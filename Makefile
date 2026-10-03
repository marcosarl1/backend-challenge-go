.PHONY: up up-full down run check-sqs-auth migrate-up migrate-down test test-race test-integration vet fmt lint build

MIGRATE_DATABASE_URL ?= postgres://wagering:wagering@db:5432/wagering?sslmode=disable

up:
	mkdir -p .local
	docker compose up -d db sqs keycloak
	HOST_UID=$$(id -u) HOST_GID=$$(id -g) docker compose run --rm sqs-init

# Stack completo (infra + migrations + app) sem passos manuais.
# Não use junto com `make run`: ambos publicam as portas 8081/9090.
up-full:
	mkdir -p .local
	HOST_UID=$$(id -u) HOST_GID=$$(id -g) docker compose up --build -d

run:
	@test -f .local/sqs-service.env || { echo "execute make up antes de make run"; exit 1; }
	@set -a; . ./.local/sqs-service.env; set +a; go run ./cmd/wagering

check-sqs-auth:
	docker compose run --rm sqs-auth-check

down:
	docker compose down

migrate-up:
	docker compose run --rm migrate -path /migrations -database "$(MIGRATE_DATABASE_URL)" up

migrate-down:
	docker compose run --rm migrate -path /migrations -database "$(MIGRATE_DATABASE_URL)" down 1

test:
	go test ./...

test-race:
	go test -race ./...

test-integration:
	go test -race -tags=integration -count=1 ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run ./...; else echo "golangci-lint not installed, skipping"; fi

build:
	go build ./...
