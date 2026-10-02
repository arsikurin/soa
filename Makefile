ARCHITECTURE_COMPOSE = docker compose -f docker-compose.architecture.yml
WORKSPACE = /usr/local/structurizr/workspace.dsl

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
