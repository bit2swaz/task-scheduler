// Package main is the entry point for the distributed task scheduler node.
// It bootstraps configuration, initialises all subsystems, and starts the node.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bit2swaz/task-scheduler/internal/api"
	"github.com/bit2swaz/task-scheduler/internal/config"
	"github.com/bit2swaz/task-scheduler/internal/election"
	"github.com/bit2swaz/task-scheduler/internal/executor"
	"github.com/bit2swaz/task-scheduler/internal/hashring"
	"github.com/bit2swaz/task-scheduler/internal/metrics"
	"github.com/bit2swaz/task-scheduler/internal/registry"
	"github.com/bit2swaz/task-scheduler/internal/scheduler"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	log.Printf("starting node %s on port %s", cfg.NodeID, cfg.Port)

	rdb, err := config.NewRedisClient(ctx, cfg)
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	defer func() { _ = rdb.Close() }()

	// subsystems
	reg := registry.New(rdb)
	ring := hashring.New(cfg.HashRingReplicas)
	exec := executor.New(rdb, cfg.NodeID, executor.Options{})
	met := metrics.NewDefault()
	sched := scheduler.New(reg, exec, ring, cfg.NodeID)

	// register this node in the cluster membership set
	if err := reg.RegisterNode(ctx, cfg.NodeID); err != nil {
		return fmt.Errorf("register node: %w", err)
	}
	defer func() {
		dCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = reg.DeregisterNode(dCtx, cfg.NodeID)
	}()

	// seed the hash ring with all known cluster members
	if nodes, err := reg.ListNodes(ctx); err == nil {
		for _, n := range nodes {
			ring.Add(n)
		}
		met.SetNodesTotal(len(nodes))
	}

	// leader election: start scheduler on election, stop on eviction
	elector := election.New(rdb, cfg.NodeID, election.ElectorOptions{
		LeaseTTL:     cfg.LeaseTTL,
		PollInterval: cfg.PollInterval,
		OnElected: func() {
			log.Printf("[%s] elected as leader — loading tasks", cfg.NodeID)
			met.SetLeader(true)
			loadCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := sched.LoadAndSchedule(loadCtx); err != nil {
				log.Printf("[%s] load tasks: %v", cfg.NodeID, err)
			}
			met.SetActiveTasks(sched.ScheduledCount())
		},
		OnEvicted: func() {
			log.Printf("[%s] evicted from leadership — stopping scheduler", cfg.NodeID)
			met.SetLeader(false)
			sched.Stop()
			met.SetActiveTasks(0)
		},
	})

	// HTTP server
	h := api.NewHandler(api.Config{
		Tasks:          reg,
		Ring:           ring,
		Exec:           exec,
		Nodes:          reg,
		Election:       elector,
		Sched:          sched,
		JWTSecret:      cfg.JWTSecret,
		NodeID:         cfg.NodeID,
		MetricsHandler: met.Handler(),
	})
	router := api.NewRouter(h)

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// start election loop in background
	go elector.Run(ctx)

	// start HTTP server in background
	srvErr := make(chan error, 1)
	go func() {
		log.Printf("[%s] http listening on :%s", cfg.NodeID, cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			srvErr <- err
		}
	}()

	// wait for shutdown signal or server error
	select {
	case <-ctx.Done():
		log.Printf("[%s] shutdown signal received", cfg.NodeID)
	case err := <-srvErr:
		return fmt.Errorf("http server: %w", err)
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
	sched.Stop()
	return nil
}
