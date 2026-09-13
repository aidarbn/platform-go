package scaffold

import "fmt"

// makefile is created once and belongs to the project afterwards.
const makefile = `# Local variables: .env when present, the generated example otherwise.
ENV_FILE ?= $(if $(wildcard .env),.env,.env.example)
include $(ENV_FILE)
export

.PHONY: help up down generate build run test lint tidy

help: ## list targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-10s %s\n", $$1, $$2}'

up: ## start local services and wait until they are healthy
	docker compose up -d --wait

down: ## stop local services
	docker compose down

generate: ## regenerate module wiring
	go tool platformgo generate

build: generate ## build the binary into bin/app
	go build -o bin/app ./cmd/app

run: generate ## run
	go run ./cmd/app

test: ## tests
	go test -race ./...

lint: ## checks, including generation freshness
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "run gofmt -w ."; exit 1; }
	go vet ./...
	go tool platformgo generate --check

tidy: ## dependencies
	go mod tidy
`

const gitignore = `/bin/
/dist/
*.test
.env
.DS_Store
`

const dockerignore = `.git
.github
bin
dist
.env
docker-compose*.yml
`

// dockerfile is created once and belongs to the project afterwards: a service may need
// extra packages or files in its image.
func dockerfile(o Options) string {
	return fmt.Sprintf(`# syntax=docker/dockerfile:1
FROM golang:%s-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/app

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/app"]
`, o.GoVersion)
}

// ciWorkflow is created once and belongs to the project afterwards.
const ciWorkflow = `name: ci

on:
  push:
    branches: [main]
  pull_request:

jobs:
  ci:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
      - run: test -z "$(gofmt -l .)"
      - run: go vet ./...
      - run: go tool platformgo generate --check
      - run: go test -race ./...
      - run: go build ./...
`

func readme(o Options) string {
	return fmt.Sprintf(`# %s

A project built on platform-go.

## Usage

	make up       # start the database and other local services
	make run      # run with .env, or .env.example when there is no .env
	make test     # tests
	make lint     # checks, including generation freshness

## Layout

- %s lists the enabled modules and their settings; run `+"`make generate`"+` after editing it
- cmd/app/main.go and cmd/app/wire.go are the hand written entry point and wiring
- cmd/app/*.gen.go, .env.example and docker-compose.yml are generated, do not edit them;
  services of your own go into docker-compose.override.yml
- Dockerfile, Makefile and .github/workflows/ci.yml are created once and belong to the project
`, o.Service, specFileName)
}
