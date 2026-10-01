.PHONY: up down migrate-up migrate-down test test-race test-integration vet fmt lint build

up:
	docker compose up --build

down:
	docker compose down

migrate-up:
	@echo "migrations not implemented yet (T2.1)"

migrate-down:
	@echo "migrations not implemented yet (T2.1)"

test:
	go test ./...

test-race:
	go test -race ./...

test-integration:
	go test -race -tags=integration ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run ./...; else echo "golangci-lint not installed, skipping"; fi

build:
	go build ./...
