MODULE     := github.com/kerpe-l/gophprofile
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BIN_DIR    := bin

LDFLAGS := -s -w \
	-X $(MODULE)/internal/buildinfo.version=$(VERSION) \
	-X $(MODULE)/internal/buildinfo.buildDate=$(BUILD_DATE)

.PHONY: all build build-server build-worker build-migrator \
        run-server run-worker run-migrator \
        docker-build up down logs \
        test test-integration lint fmt bench cover tidy clean

all: lint test build

build: build-server build-worker build-migrator

build-server:
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/server ./cmd/server

build-worker:
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/worker ./cmd/worker

build-migrator:
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/migrator ./cmd/migrator

run-server: build-server
	./$(BIN_DIR)/server

run-worker: build-worker
	./$(BIN_DIR)/worker

run-migrator: build-migrator
	./$(BIN_DIR)/migrator

docker-build:
	VERSION=$(VERSION) BUILD_DATE=$(BUILD_DATE) docker compose build

up:
	VERSION=$(VERSION) BUILD_DATE=$(BUILD_DATE) docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f

test:
	go test ./... -race

test-integration:
	go test -tags=integration ./... -race

lint:
	golangci-lint run

fmt:
	golangci-lint fmt

bench:
	go test ./... -run '^$$' -bench . -benchmem

cover:
	go test ./... -race -coverprofile=coverage.out -covermode=atomic
	go tool cover -func=coverage.out | tail -1

tidy:
	go mod tidy

clean:
	rm -rf $(BIN_DIR) coverage.out
