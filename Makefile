SHELL := /bin/sh
.SHELLFLAGS := -eu -c

# ---------------------------------------------------------------------------
# iam-tender-acl build tooling
#
# Structured to mirror iam-group-mapping's Makefile conventions (config
# block, .env sourcing, tidy/fmt-check/vet/lint/test-ci pipeline, per-suite
# test targets, coverage, docker-up/down, godoc, pin-base-images) so
# switching between IAM repos feels the same.
#
# This service uses Clean Architecture / Ports-and-Adapters package layout
# (internal/core/{domain,port,service}, internal/adapter/{inbound,outbound}),
# matching every sibling IAM service — see ARCHITECTURE.md's "Layer model"
# section for the history of the earlier flat layout (LLD §6, TAC-D1) this
# supersedes. Swagger UI (generated from handler annotations, `make swag`)
# mirrors iam-org-membership's setup — see the DOCS section below. A
# reconciler binary is the one target intentionally still omitted — see
# README.md.
# ---------------------------------------------------------------------------

# -----------------------------
# CONFIG
# -----------------------------
-include .env

APP_NAME      ?= iam-tender-acl
APP_ENV       ?= dev
GO            ?= go
BUILD_VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# platform-gincommon/platform-pgcommon/platform-events are private
# github.com/BCBP-SOLUTIONS-FZC-LLC modules, not vendored — fetched via git
# using SSH or a GO_PRIVATE_TOKEN-backed credential helper.
export GOPRIVATE ?= github.com/BCBP-SOLUTIONS-FZC-LLC/*
export GONOSUMDB ?= github.com/BCBP-SOLUTIONS-FZC-LLC/*

export APP_NAME APP_ENV BUILD_VERSION

MODULE    := github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl
BINARY    := tender-acl
CMD_PKG   := ./cmd/tender-acl
BUILD_DIR := bin
GOFLAGS   ?=
LDFLAGS   := -s -w -X main.buildVersion=$(BUILD_VERSION)
IMAGE     ?= iam-tender-acl:latest

ALL_TEST_TAGS := integration,rls,e2e

# Test package groups — this service separates suites by build tag/
# directory (integration / rls / e2e) rather than by name pattern.
TEST_INTEGRATION_PKGS := ./test/integration/...
TEST_RLS_PKGS         := ./test/rls/...
TEST_E2E_PKGS         := ./test/e2e/...

# There is no separate test/unit/ directory: every white-box (package-
# internal) test is colocated under internal/**, so plain "./..." already
# IS the unit-test surface — integration/rls/e2e contribute "no test files"
# to an untagged run since their *_test.go files are all //go:build-gated.
TEST_UNIT_PKGS := ./...

COVER_PKG_LIST := $(shell $(GO) list ./internal/... 2>/dev/null | tr '\n' ',' | sed 's/,$$//')

# The migration role MAY have BYPASSRLS; in docker-compose that's simply the
# postgres superuser (tender_acl). Production points this at a dedicated,
# more privileged migration role (tender_acl_migrator) provisioned by
# infrastructure tooling, never at the runtime app role (tender_acl_app).
DATABASE_MIGRATION_URL ?= postgres://tender_acl:tender_acl@localhost:5432/tender_acl?sslmode=disable
# Migrations live under the outbound Postgres adapter (LLD §7.4), not a
# top-level migrations/ directory.
MIGRATIONS_DIR := internal/adapter/outbound/postgres/migrations
MIGRATE_IMAGE  := migrate/migrate:v4.17.1

.PHONY: all setup install-hooks help godoc pin-base-images \
        tidy fmt fmt-check vet mod-verify vuln-check lint \
        build run clean \
        test test-unit test-integration test-rls test-e2e \
        _test-unit-plain _test-integration-plain _test-rls-plain \
        race _race-unit _race-integration _race-rls _race-e2e \
        test-ci _test-unit-cov _test-integration-cov _test-rls-cov _merge-coverage \
        cover cover-func \
        docker-build docker-push docker-up docker-down compose-up compose-down \
        migrate-up migrate-down migrate-create \
        generate ci

all: build

# -----------------------------
# SETUP
# -----------------------------

setup:
	@test -f .env || cp .env.example .env
	@mkdir -p .git/hooks
	@test -f .githooks/pre-commit && cp .githooks/pre-commit .git/hooks/pre-commit && chmod +x .git/hooks/pre-commit || true
	@echo "Environment ready (.env)"

install-hooks:
	@mkdir -p .git/hooks
	@cp .githooks/pre-commit .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@echo "Installed git hooks"

godoc:
	@echo "Starting pkgsite at http://localhost:8080 — press Ctrl-C to stop"
	$(GO) run golang.org/x/pkgsite/cmd/pkgsite@latest -open .

# pin-base-images: fetch and pin the current SHA digests for Dockerfile base
# images. Writes the digests both to the Dockerfile FROM lines and to
# .docker-digests (a checked-in provenance record). CI can verify the two
# match.
pin-base-images:
	@echo "Fetching SHA digests for Dockerfile base images..."
	@GOLANG_DIGEST=$$(docker buildx imagetools inspect golang:1.26.6-bookworm --format '{{.Manifest.Digest}}') && \
	 DISTROLESS_DIGEST=$$(docker buildx imagetools inspect gcr.io/distroless/static-debian12:nonroot --format '{{.Manifest.Digest}}') && \
	 sed -E -i.bak \
	   -e "s|FROM golang:1\.26\.6-bookworm(@sha256:[a-f0-9]+)?|FROM golang:1.26.6-bookworm@$$GOLANG_DIGEST|" \
	   -e "s|FROM gcr\.io/distroless/static-debian12:nonroot(@sha256:[a-f0-9]+)?|FROM gcr.io/distroless/static-debian12:nonroot@$$DISTROLESS_DIGEST|" \
	   Dockerfile && rm -f Dockerfile.bak && \
	 echo "golang:1.26.6-bookworm $$GOLANG_DIGEST" > .docker-digests && \
	 echo "gcr.io/distroless/static-debian12:nonroot $$DISTROLESS_DIGEST" >> .docker-digests && \
	 echo "Digests written to .docker-digests — commit both Dockerfile and .docker-digests"

help:
	@echo "Available commands:"
	@echo "  make setup           - copy .env.example to .env if missing, install git hooks"
	@echo "  make install-hooks   - install .githooks/pre-commit into .git/hooks"
	@echo "  make tidy            - go mod tidy"
	@echo "  make fmt             - format source with gofmt"
	@echo "  make fmt-check       - verify gofmt formatting (mirrors CI)"
	@echo "  make vet             - go vet (default build + every test build tag)"
	@echo "  make lint            - run golangci-lint (via go tool)"
	@echo "  make mod-verify      - go mod verify"
	@echo "  make vuln-check      - govulncheck on cmd/ + internal/"
	@echo "  make test            - unit + integration + rls tests, in parallel (requires Docker)"
	@echo "  make test-unit       - unit tests only (no Docker required)"
	@echo "  make test-integration- integration tests: Postgres+Valkey+SQS-compatible via Testcontainers (requires Docker)"
	@echo "  make test-rls        - Postgres Row-Level-Security tests via Testcontainers (requires Docker)"
	@echo "  make test-e2e        - end-to-end tests: Postgres+Valkey+SQS-compatible via Testcontainers (requires Docker)"
	@echo "  make race            - all four suites with -race, in parallel (requires Docker)"
	@echo "  make test-ci         - race + coverage, merged into coverage.out (used in CI, requires Docker)"
	@echo "  make cover           - coverage HTML report"
	@echo "  make cover-func      - coverage summary by function"
	@echo "  make run             - run the service locally (go run), sourcing .env if present"
	@echo "  make build           - compile the tender-acl binary to bin/"
	@echo "  make ci              - tidy + fmt-check + vet + lint + test-ci + build"
	@echo "  make docker-build    - build the container image (IMAGE=repo:tag to override)"
	@echo "  make docker-push     - push the container image"
	@echo "  make docker-up       - start local Postgres + Valkey + LocalStack (for \`make run\` against a live stack)"
	@echo "  make docker-down     - stop containers started by docker-up/compose-up"
	@echo "  make compose-up      - start the full local dev stack (postgres, valkey, localstack, service — service self-migrates at startup)"
	@echo "  make compose-down    - stop and remove the local dev stack, including volumes"
	@echo "  make migrate-up      - apply all pending migrations against DATABASE_MIGRATION_URL (manual/CI use; the binary also self-migrates at startup)"
	@echo "  make migrate-down    - roll back one migration against DATABASE_MIGRATION_URL"
	@echo "  make migrate-create  - create a new migration pair (NAME=add_foo_table)"
	@echo "  make godoc           - serve local godoc/pkgsite at http://localhost:8080"
	@echo "  make pin-base-images - fetch + pin SHA digests for Dockerfile base images"
	@echo "  make generate        - run any go:generate directives (currently none)"
	@echo "  make swag            - regenerate docs/swagger/ from handler annotations (mirrors iam-org-membership)"
	@echo "  make swag-check      - fail if Swagger regeneration would change docs/swagger/ (CI drift gate)"
	@echo "  make clean           - remove build artifacts and coverage output"

# -----------------------------
# GO BASICS
# -----------------------------

tidy:
	$(GO) mod tidy

fmt:
	gofmt -s -w .

fmt-check:
	@unformatted=$$(gofmt -l cmd/ internal/ test/ 2>/dev/null); \
	if [ -n "$$unformatted" ]; then \
		echo "FAIL: unformatted files:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	@echo "gofmt: all files formatted"

vet:
	$(GO) vet ./...
	$(GO) vet -tags=$(ALL_TEST_TAGS) ./...

mod-verify:
	$(GO) mod verify

vuln-check:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./cmd/... ./internal/...

lint:
	$(GO) tool golangci-lint run ./...
	$(GO) tool golangci-lint run --build-tags=$(ALL_TEST_TAGS) ./...

# -----------------------------
# BUILD / RUN
# -----------------------------

build:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -trimpath -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) $(CMD_PKG)
	$(GO) build -tags=$(ALL_TEST_TAGS) ./...

run:
	@-lsof -ti :$${HTTP_PORT:-8080} | xargs kill -9 2>/dev/null; true
	@if [ -f .env ]; then set -a && . ./.env && set +a && $(GO) run $(CMD_PKG); else $(GO) run $(CMD_PKG); fi

clean:
	rm -rf $(BUILD_DIR) .coverage
	rm -f coverage.out coverage.html

# -----------------------------
# TESTS
# -----------------------------

.coverage:
	@mkdir -p .coverage

test:
	$(MAKE) -j3 _test-unit-plain _test-integration-plain _test-rls-plain

_test-unit-plain:
	$(GO) test $(TEST_UNIT_PKGS) -count=1 -timeout 120s
_test-integration-plain:
	$(GO) test $(TEST_INTEGRATION_PKGS) -tags=integration -count=1 -timeout 300s
_test-rls-plain:
	$(GO) test $(TEST_RLS_PKGS) -tags=rls -count=1 -timeout 300s

test-unit:
	$(GO) test $(TEST_UNIT_PKGS) -count=1 -timeout 120s -v

test-integration:
	$(GO) test $(TEST_INTEGRATION_PKGS) -tags=integration -count=1 -timeout 300s -v

test-rls:
	$(GO) test $(TEST_RLS_PKGS) -tags=rls -count=1 -timeout 300s -v

test-e2e:
	$(GO) test $(TEST_E2E_PKGS) -tags=e2e -count=1 -timeout 300s -v

race:
	$(MAKE) -j4 _race-unit _race-integration _race-rls _race-e2e

_race-unit:
	$(GO) test $(TEST_UNIT_PKGS) -race -count=1 -timeout 120s
_race-integration:
	$(GO) test $(TEST_INTEGRATION_PKGS) -tags=integration -race -count=1 -timeout 300s
_race-rls:
	$(GO) test $(TEST_RLS_PKGS) -tags=rls -race -count=1 -timeout 300s
_race-e2e:
	$(GO) test $(TEST_E2E_PKGS) -tags=e2e -race -count=1 -timeout 300s

_test-unit-cov: | .coverage
	$(GO) test $(TEST_UNIT_PKGS) -race -count=1 -timeout 120s -coverpkg=$(COVER_PKG_LIST) -coverprofile=.coverage/unit.out
_test-integration-cov: | .coverage
	$(GO) test $(TEST_INTEGRATION_PKGS) -tags=integration -race -count=1 -timeout 300s -coverpkg=$(COVER_PKG_LIST) -coverprofile=.coverage/integration.out
_test-rls-cov: | .coverage
	$(GO) test $(TEST_RLS_PKGS) -tags=rls -race -count=1 -timeout 300s -coverpkg=$(COVER_PKG_LIST) -coverprofile=.coverage/rls.out

# Merge the three per-suite profiles into a single coverage.out (max-count
# strategy — any suite covering a block wins). Mirrors iam-group-mapping.
_merge-coverage:
	@python3 scripts/merge_coverage.py \
	  .coverage/unit.out .coverage/integration.out .coverage/rls.out \
	  > coverage.out
	@echo "==> coverage.out merged from all suites (max-count strategy)"

test-ci: | .coverage
	$(MAKE) -j3 _test-unit-cov _test-integration-cov _test-rls-cov
	$(MAKE) _merge-coverage

cover: test-ci
	$(GO) tool cover -html=coverage.out

cover-func: test-ci
	$(GO) tool cover -func=coverage.out

# -----------------------------
# DOCKER
# -----------------------------

docker-build:
	docker build -t $(IMAGE) .

docker-push: docker-build
	docker push $(IMAGE)

docker-up:
	docker compose up -d postgres valkey localstack

docker-down:
	docker compose down

compose-up:
	docker compose up --build -d

compose-down:
	docker compose down -v

# -----------------------------
# MIGRATIONS
# -----------------------------

migrate-up:
	docker run --rm -v $(CURDIR)/$(MIGRATIONS_DIR):/migrations --network host \
		$(MIGRATE_IMAGE) -path=/migrations -database="$(DATABASE_MIGRATION_URL)" up

migrate-down:
	docker run --rm -v $(CURDIR)/$(MIGRATIONS_DIR):/migrations --network host \
		$(MIGRATE_IMAGE) -path=/migrations -database="$(DATABASE_MIGRATION_URL)" down 1

migrate-create:
	docker run --rm -v $(CURDIR)/$(MIGRATIONS_DIR):/migrations \
		$(MIGRATE_IMAGE) create -ext sql -dir /migrations -seq $(NAME)

# -----------------------------
# DOCS
# -----------------------------
# OpenAPI and AsyncAPI specs have different sources of truth, mirroring
# iam-org-membership exactly:
#   - OpenAPI (docs/swagger/{docs.go,swagger.json,swagger.yaml}) is GENERATED
#     from swag `@Summary`/`@Tags`/`@Router` annotations on handler functions
#     via `make swag` — never hand-edited. docs.go's init() registers it;
#     ginSwagger.WrapHandler serves it at /swagger/*any (router.go).
#   - AsyncAPI (api/asyncapi.yaml) IS hand-maintained — there is no
#     //go:embed of it and no served page for it in this service (unlike
#     iam-org-membership's /asyncapi Studio page); keep it in sync with the
#     actual event contract by hand when it changes.
# Serves:
#   /swagger    Swagger UI rendering the generated OpenAPI spec (REST — Try it out enabled)
#   /healthz    liveness
#   /readyz     readiness

# swag: generate the OpenAPI/Swagger 2.0 spec from // @… annotations on
# handlers under cmd/tender-acl and internal/adapter/inbound/http. Mirrors
# iam-org-membership's identical pattern. scripts/patch-swagger-extensions.py
# then injects a top-level `tags` array (swaggo parses @tag.name/
# @tag.description but never emits them) to pin the Swagger UI group order —
# public, internal, infra last — since it would otherwise default to
# first-appearance-in-paths order. The output under docs/swagger/ is
# checked into the repo; CI's swag-check target fails a PR whose
# annotations drift from what's on disk.
.PHONY: swag
swag:
	@echo "Generating Swagger docs..."
	$(GO) tool swag init \
	  -g swagger_info.go \
	  -d cmd/tender-acl,internal/adapter/inbound/http \
	  --output docs/swagger \
	  --parseDependency \
	  --parseInternal
	@python3 scripts/patch-swagger-extensions.py
	@echo "Swagger docs written to docs/swagger/"

.PHONY: swag-check
swag-check:
	bash .github/scripts/check-swagger-stale.sh

# -----------------------------
# CI
# -----------------------------

ci: tidy fmt-check vet lint test-ci build

generate:
	$(GO) generate ./...
