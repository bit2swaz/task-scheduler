// Package election implements Redis-based leader election using SET NX EX
// with periodic lease renewal and graceful failover detection.
package election
