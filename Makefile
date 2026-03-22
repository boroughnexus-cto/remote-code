.PHONY: build clean dev frontend backend install run restart sqlc-generate test test-race test-swarm vet lint ci

GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)

build: frontend backend

frontend:
	cd frontend && npm run build

backend:
	go build -ldflags="-X main.BuildCommit=$(GIT_COMMIT)" -o swarmops .

dev:
	go run .

install:
	cd frontend && npm install

sqlc-generate:
	~/go/bin/sqlc generate

clean:
	rm -rf static/*
	rm -f swarmops
	rm -f swarmops.db
	rm -f swarmops-test*.db

run: build
	@echo "Killing any process running on port 8080..."
	@fuser -k 8080/tcp 2>/dev/null || true
	./swarmops

restart: build
	@fuser -k 8080/tcp 2>/dev/null || true
	systemctl --user restart swarmops
	@echo "SwarmOps restarted ($(GIT_COMMIT))"

## test: run all tests
test:
	go test -timeout 120s -count=1 ./...

## test-race: run all tests with race detector
test-race:
	go test -race -timeout 120s -count=1 ./...

## test-swarm: run swarm subsystem tests only
test-swarm:
	go test -race -timeout 120s -count=1 -run 'TestSwarm|TestTask|TestGoal|TestEscalation|TestReadNew|TestProcess|TestHandoff|TestCheck|TestValid' ./...

## vet: static analysis
vet:
	go vet ./...

## lint: golangci-lint (if installed)
lint:
	@command -v golangci-lint >/dev/null 2>&1 \
		&& golangci-lint run ./... \
		|| echo "golangci-lint not installed — skipping"

## ci: full CI gate: vet + race-enabled tests
ci: vet test-race