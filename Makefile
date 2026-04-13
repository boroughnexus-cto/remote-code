.PHONY: build clean dev run restart test test-race vet lint ci stop-port

GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)

build:
	go build -ldflags="-X main.BuildCommit=$(GIT_COMMIT)" -o swarmops .

dev:
	go run .

clean:
	rm -f swarmops
	rm -f swarmops.db
	rm -f swarmops-test*.db*

# stop-port gracefully stops whatever is listening on 8080
stop-port:
	@pid=$$(fuser 8080/tcp 2>/dev/null | xargs); \
	if [ -n "$$pid" ]; then \
		echo "Port 8080 in use by PID $$pid — sending SIGTERM..."; \
		kill $$pid 2>/dev/null || true; \
		for i in 1 2 3 4 5 6; do \
			fuser 8080/tcp >/dev/null 2>&1 || { echo "Port 8080 free."; exit 0; }; \
			sleep 1; \
		done; \
		echo "Still running after 6s — sending SIGKILL..."; \
		kill -9 $$pid 2>/dev/null || true; \
		sleep 1; \
	fi

run: build stop-port
	./swarmops

restart: build stop-port
	systemctl --user restart swarmops
	@for i in 1 2 3 4 5; do \
		sleep 1; \
		if curl -sf http://localhost:8080/api/dashboard/stats >/dev/null 2>&1; then \
			echo "SwarmOps restarted successfully ($(GIT_COMMIT))"; \
			exit 0; \
		fi; \
	done; \
	echo "WARNING: SwarmOps may not have started — check: journalctl --user -u swarmops -n 20"

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
