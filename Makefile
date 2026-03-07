.PHONY: build clean dev frontend backend install run sqlc-generate

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