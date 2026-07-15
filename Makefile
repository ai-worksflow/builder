COMPOSE ?= docker compose
APP_SERVICES := api frontend
DEPLOY_SERVICES := $(APP_SERVICES) nginx
WAIT_TIMEOUT ?= 300
LOG_TAIL ?= 200
BUILD_ARGS ?=
UP_ARGS ?=

.DEFAULT_GOAL := help

.PHONY: help compose-check build deploy deploy-fresh status logs down

help:
	@printf '%s\n' \
		'make deploy        Rebuild the frontend and API, then deploy the stack' \
		'make deploy-fresh  Rebuild the frontend and API without cache, then deploy' \
		'make build         Build the frontend and API images only' \
		'make status        Show Compose service status' \
		'make logs          Follow frontend, API, and Nginx logs' \
		'make down          Stop the stack without deleting persisted data'

compose-check:
	$(COMPOSE) config --quiet

build: compose-check
	$(COMPOSE) build $(BUILD_ARGS) $(APP_SERVICES)

deploy: compose-check
	$(COMPOSE) up --build --detach --remove-orphans --wait --wait-timeout $(WAIT_TIMEOUT) $(UP_ARGS) $(DEPLOY_SERVICES)
	$(COMPOSE) ps

deploy-fresh: compose-check
	$(COMPOSE) build --no-cache $(BUILD_ARGS) $(APP_SERVICES)
	$(COMPOSE) up --detach --remove-orphans --wait --wait-timeout $(WAIT_TIMEOUT) $(UP_ARGS) $(DEPLOY_SERVICES)
	$(COMPOSE) ps

status:
	$(COMPOSE) ps

logs:
	$(COMPOSE) logs --follow --tail=$(LOG_TAIL) $(DEPLOY_SERVICES)

down:
	$(COMPOSE) down
