.PHONY: help test test-db lint fmt tidy ci proto-test

help: ## list targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-10s %s\n", $$1, $$2}'

test: ## tests with the race detector
	go test -race ./...

test-db: ## tests including the ones against a real database (DATABASE_TEST_URL)
	@test -n "$(DATABASE_TEST_URL)" || { echo "set DATABASE_TEST_URL, for example postgres://postgres:pg@127.0.0.1:5432/platformgo?sslmode=disable"; exit 1; }
	go test -race -count=1 ./...

lint: ## go vet
	go vet ./...

fmt: ## formatting
	gofmt -l -w .

proto-test: ## regenerate the test service of the api module
	cd kit/internal/testapi && go run github.com/bufbuild/buf/cmd/buf@v1.73.0 generate

tidy: ## dependencies
	go mod tidy

ci: lint test ## what CI runs
