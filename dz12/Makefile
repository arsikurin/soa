ARCHITECTURE_COMPOSE = docker compose -f docker-compose.architecture.yml
WORKSPACE = /usr/local/structurizr/workspace.dsl
COMPOSE = docker compose

.PHONY: generate build test test-scripts check up down e2e

generate:
	go generate ./...

build: generate
	go build -trimpath -o bin/marketplace ./cmd/marketplace

test: generate
	go test ./...

test-scripts:
	python3 -m unittest discover -s scripts -p 'test_*.py'

check: generate test-scripts
	go test -race ./...
	go vet ./...

up:
	$(COMPOSE) up --build -d --wait

down:
	$(COMPOSE) down

e2e:
	python3 scripts/e2e.py

.PHONY: architecture-validate architecture-export architecture-up architecture-down

architecture-validate:
	$(ARCHITECTURE_COMPOSE) run --rm structurizr validate -workspace $(WORKSPACE)

architecture-export: architecture-validate
	$(ARCHITECTURE_COMPOSE) run --rm structurizr export -workspace $(WORKSPACE) -format svg -output /usr/local/structurizr
	$(ARCHITECTURE_COMPOSE) run --rm structurizr export -workspace $(WORKSPACE) -format png -output /usr/local/structurizr

architecture-up:
	$(ARCHITECTURE_COMPOSE) up -d

architecture-down:
	$(ARCHITECTURE_COMPOSE) down
