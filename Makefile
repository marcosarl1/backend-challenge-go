.PHONY: up down migrate-up migrate-down test test-race test-integration vet fmt lint build

DATABASE_URL ?= postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable
MIGRATE_IMAGE ?= docker.io/migrate/migrate:v4.20.1

up:
	docker compose up --build

down:
	docker compose down

migrate-up:
	docker run --rm --network host -v ./migrations:/migrations $(MIGRATE_IMAGE) -path /migrations -database "$(DATABASE_URL)" up

migrate-down:
	docker run --rm --network host -v ./migrations:/migrations $(MIGRATE_IMAGE) -path /migrations -database "$(DATABASE_URL)" down 1

test:
	go test ./...

test-race:
	go test -race ./...

test-integration:
	TEST_DATABASE_URL="$(DATABASE_URL)" TEST_KEYCLOAK_URL="http://localhost:8080" go test -race ./test/integration/

vet:
	go vet ./...

fmt:
	gofmt -l -w .

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run ./...; else echo "golangci-lint not installed, skipping"; fi

build:
	go build ./...
