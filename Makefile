.PHONY: build test test-race test-integration lint run docker-up docker-down

BINARY     := scheduler
BUILD_DIR  := bin
CMD        := ./cmd/scheduler
COMPOSE    := docker compose -f docker/docker-compose.yml

build:
	go build -o $(BUILD_DIR)/$(BINARY) $(CMD)

test:
	go test ./...

test-race:
	go test -race ./...

test-integration:
	go test -tags integration ./...

lint:
	golangci-lint run

run:
	go run $(CMD)

docker-up:
	$(COMPOSE) up --build

docker-down:
	$(COMPOSE) down -v

clean:
	rm -rf $(BUILD_DIR)
