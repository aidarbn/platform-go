.PHONY: help test lint fmt tidy ci

help: ## list targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-10s %s\n", $$1, $$2}'

test: ## tests with the race detector
	go test -race ./...

lint: ## go vet
	go vet ./...

fmt: ## formatting
	gofmt -l -w .

tidy: ## dependencies
	go mod tidy

ci: lint test ## what CI runs
