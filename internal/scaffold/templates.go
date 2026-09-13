package scaffold

import "fmt"

// makefile is created once and belongs to the project afterwards. Its targets follow
// taply; the checks behind lint depend on the enabled modules, so platformgo lint decides
// what runs and the Makefile stays the same when modules change.
const makefile = `# Local variables: .env when present, the generated example otherwise.
ENV_FILE ?= $(if $(wildcard .env),.env,.env.example)
include $(ENV_FILE)
export

# Branch the proto files are checked against for breaking changes.
PROTO_BASE ?=

.PHONY: help up down generate db-generate migration build run test lint ci tidy

help: ## list targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-12s %s\n", $$1, $$2}'

up: ## start local services and wait until they are healthy
	docker compose up -d --wait

down: ## stop local services
	docker compose down

generate: ## regenerate wiring, queries and API code
	go tool platformgo generate

db-generate: up ## migrate the local database and regenerate the dynamic query builder
	go tool platformgo db generate

migration: ## add a migration: make migration name=create_orders
	go tool platformgo migrate create $(name)

build: generate ## build the binary into bin/app
	go build -o bin/app ./cmd/app

run: generate ## run
	go run ./cmd/app

test: ## tests
	go test -race ./...

lint: ## format, tidy, build, generation, file length, golangci-lint, proto, govulncheck
	go tool platformgo lint $(if $(PROTO_BASE),--proto-against $(PROTO_BASE))

ci: lint test ## what CI runs

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

// dockerfile is created once and belongs to the project afterwards. It follows taply:
// BuildKit caches for modules and the build, cross compilation for the target platform,
// and an Alpine runtime with CA certificates and time zones, so a service can add its
// own certificates.
func dockerfile(o Options) string {
	return fmt.Sprintf(`# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:%s-alpine AS builder
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/app

FROM alpine:3.24
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /out/app /usr/local/bin/app
USER 1000:1000
EXPOSE 9090 8080
ENTRYPOINT ["/usr/local/bin/app"]
`, o.GoVersion)
}

// golangci is the linter configuration of taply.
const golangci = `version: "2"

run:
  timeout: 5m
  tests: true

linters:
  enable:
    - govet
    - staticcheck
    - errcheck
    - ineffassign
    - unused
    - gosec
  settings:
    gosec:
      excludes:
        - G115 # integer conversion of ids between int64 and int32

formatters:
  enable:
    - gofmt
  settings:
    gofmt:
      simplify: true

issues:
  max-issues-per-linter: 0
  max-same-issues: 0
`

// ciWorkflow is created once and belongs to the project afterwards. As in taply the
// local services come up from docker-compose.yml and make ci runs the checks and tests.
const ciWorkflow = `name: ci

on:
  push:
    branches: [main]
  pull_request:

concurrency:
  group: ci-${{ github.head_ref || github.ref_name }}
  cancel-in-progress: true

jobs:
  ci:
    runs-on: ubuntu-latest
    timeout-minutes: 30
    steps:
      - uses: actions/checkout@v7
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
      - name: Local services
        run: make up
      - name: Checks and tests
        run: make ci
      - name: Stop services
        if: always()
        run: docker compose down -v --remove-orphans || true
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
