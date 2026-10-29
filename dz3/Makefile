.PHONY: generate build check up down logs e2e

generate:
	go generate ./...

build: generate
	go build -trimpath -o bin/booking ./cmd/booking
	go build -trimpath -o bin/flight ./cmd/flight

check: generate
	go test -race ./...
	go vet ./...

up:
	docker compose up --build -d --wait --wait-timeout 180

down:
	docker compose down

logs:
	docker compose logs -f booking flight

e2e: generate
	RUN_E2E=1 go test -v -count=1 ./tests/e2e
