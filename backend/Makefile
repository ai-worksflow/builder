.PHONY: build check compose-down compose-logs compose-up fmt fmt-check run test test-race tidy vet

GO_FILES := $(shell find cmd internal -type f -name '*.go' -print)

build:
	go build -trimpath -o bin/api ./cmd/api

run:
	go run ./cmd/api

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w $(GO_FILES)

fmt-check:
	@files="$$(gofmt -l $(GO_FILES))"; test -z "$$files" || (printf '%s\n' "$$files" && exit 1)

tidy:
	go mod tidy

check: fmt-check vet test

compose-up:
	docker compose -f ../docker-compose.yml up --build

compose-down:
	docker compose -f ../docker-compose.yml down

compose-logs:
	docker compose -f ../docker-compose.yml logs -f api
