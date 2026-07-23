COMPOSE ?= docker compose
APP_SERVICES := api frontend
DEPLOY_SERVICES := $(APP_SERVICES) nginx
WAIT_TIMEOUT ?= 300
LOG_TAIL ?= 200
BUILD_ARGS ?=
UP_ARGS ?=
RUNNER_GO_IMAGE ?=
RUNNER_NODE_IMAGE ?=
CODEX_VERSION ?=
CODEX_INTEGRITY ?=
AGENT_RUNNER_TAG ?= worksflow/agent-runner:local
SANDBOX_RUNNER_TAG ?= worksflow/sandbox-runner:local
K8S_DIR := deploy/k8s

.DEFAULT_GOAL := help

.PHONY: help qualification-check compose-check compose-contract-check runtime-image-contract-check runtime-images agent-runner-image sandbox-runner-image build deploy deploy-fresh status logs down k8s-bootstrap k8s-deploy k8s-verify k8s-status k8s-down

help:
	@printf '%s\n' \
		'make deploy        Rebuild the frontend and API, then deploy the stack' \
		'make deploy-fresh  Rebuild the frontend and API without cache, then deploy' \
		'make build         Build the frontend and API images only' \
		'make qualification-check  Validate acceptance ID to test/artifact coverage' \
		'make compose-contract-check  Validate Release/Agent Compose topology and passthrough' \
		'make runtime-image-contract-check  Validate immutable runner build inputs and Dockerfile wiring' \
		'make runtime-images  Build Agent and Sandbox Runners from exact digest/version/integrity inputs' \
		'make status        Show Compose service status' \
		'make logs          Follow frontend, API, and Nginx logs' \
		'make down          Stop the stack without deleting persisted data' \
		'make k8s-bootstrap Install pinned kubectl and kind into .tools/' \
		'make k8s-deploy    Create the local Kubernetes vertical slice' \
		'make k8s-verify    Verify Gateway routing and namespace isolation' \
		'make k8s-status    Show local Kubernetes and Gateway status' \
		'make k8s-down      Delete only the worksflow-local kind cluster'

qualification-check:
	node frontend/scripts/check-qualification-manifest.mjs

compose-check: qualification-check runtime-image-contract-check compose-contract-check

compose-contract-check:
	sh deploy/check-compose-contracts.sh

runtime-image-contract-check:
	sh sandbox-runner/validate-runner-build-args.sh --self-test
	sh sandbox-runner/validate-runner-build-args.sh --check-dockerfiles .

runtime-images: agent-runner-image sandbox-runner-image

agent-runner-image: runtime-image-contract-check
	sh sandbox-runner/validate-runner-build-args.sh '$(RUNNER_GO_IMAGE)' '$(RUNNER_NODE_IMAGE)' '$(CODEX_VERSION)' '$(CODEX_INTEGRITY)'
	docker build -f agent-runner/Dockerfile \
		--build-arg GO_IMAGE='$(RUNNER_GO_IMAGE)' \
		--build-arg NODE_IMAGE='$(RUNNER_NODE_IMAGE)' \
		--build-arg CODEX_VERSION='$(CODEX_VERSION)' \
		--build-arg CODEX_INTEGRITY='$(CODEX_INTEGRITY)' \
		--tag '$(AGENT_RUNNER_TAG)' .

sandbox-runner-image: runtime-image-contract-check
	sh sandbox-runner/validate-runner-build-args.sh '$(RUNNER_GO_IMAGE)' '$(RUNNER_NODE_IMAGE)' '$(CODEX_VERSION)' '$(CODEX_INTEGRITY)'
	docker build -f sandbox-runner/Dockerfile \
		--build-arg GO_IMAGE='$(RUNNER_GO_IMAGE)' \
		--build-arg NODE_IMAGE='$(RUNNER_NODE_IMAGE)' \
		--build-arg CODEX_VERSION='$(CODEX_VERSION)' \
		--build-arg CODEX_INTEGRITY='$(CODEX_INTEGRITY)' \
		--tag '$(SANDBOX_RUNNER_TAG)' sandbox-runner

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

k8s-bootstrap:
	sh $(K8S_DIR)/bootstrap-tools.sh

k8s-deploy:
	sh $(K8S_DIR)/deploy-local.sh

k8s-verify:
	sh $(K8S_DIR)/verify-local.sh

k8s-status:
	sh $(K8S_DIR)/status-local.sh

k8s-down:
	sh $(K8S_DIR)/destroy-local.sh
