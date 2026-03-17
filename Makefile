.PHONY: build clean dev frontend backend install run sqlc-generate test test-race test-swarm vet lint ci

build: frontend backend

frontend:
	cd frontend && npm run build

backend:
	go build -o remote-code .

dev:
	go run .

install:
	cd frontend && npm install

sqlc-generate:
	~/go/bin/sqlc generate

clean:
	rm -rf static/*
	rm -f remote-code
	rm -f remote-code.db
	rm -f remote-code-test*.db

run: build
	@echo "Killing any process running on port 8080..."
	@lsof -ti:8080 | xargs -r kill -9 2>/dev/null || true
	./remote-code

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