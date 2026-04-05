.PHONY: build clean dev run restart test test-race vet lint ci

GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)

build:
	go build -ldflags="-X main.BuildCommit=$(GIT_COMMIT)" -o swarmops .

dev:
	go run .

clean:
	rm -f swarmops
	rm -f swarmops.db
	rm -f swarmops-test*.db*

run: build
	@echo "Killing any process running on port 8080..."
	@fuser -k 8080/tcp 2>/dev/null || true
	./swarmops

restart: build
	@fuser -k 8080/tcp 2>/dev/null || true
	systemctl --user restart swarmops
	@echo "SwarmOps restarted ($(GIT_COMMIT))"

test:
	go test -timeout 120s -count=1 ./...

test-race:
	go test -race -timeout 120s -count=1 ./...

vet:
	go vet ./...

lint:
	@command -v golangci-lint >/dev/null 2>&1 \
		&& golangci-lint run ./... \
		|| echo "golangci-lint not installed — skipping"

ci: vet test-race
