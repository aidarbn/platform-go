.PHONY: help test lint fmt tidy ci

help: ## список целей
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-10s %s\n", $$1, $$2}'

test: ## тесты с детектором гонок
	go test -race ./...

lint: ## go vet
	go vet ./...

fmt: ## форматирование
	gofmt -l -w .

tidy: ## зависимости
	go mod tidy

ci: lint test ## то же, что в CI
