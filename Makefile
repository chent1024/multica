.PHONY: help makehelp dev server daemon cli multica build test migrate-up migrate-down seed clean setup start stop check worktree-env setup-main start-main stop-main check-main setup-worktree start-worktree stop-worktree check-worktree remove-worktree db-up db-down db-drop db-reset up down status list destroy gc env-exec api-dev desktop-dev

MAIN_ENV_FILE ?= .env
WORKTREE_ENV_FILE ?= .env.worktree
ENV_FILE ?= $(if $(wildcard $(MAIN_ENV_FILE)),$(MAIN_ENV_FILE),$(if $(wildcard $(WORKTREE_ENV_FILE)),$(WORKTREE_ENV_FILE),$(MAIN_ENV_FILE)))

ifneq ($(wildcard $(ENV_FILE)),)
include $(ENV_FILE)
endif

MULTICA_SQLITE_PATH ?= $(CURDIR)/.multica/multica.db
PORT := $(or $(BACKEND_PORT),$(API_PORT),$(SERVER_PORT),$(PORT),17824)
ifeq ($(origin MULTICA_PUBLIC_URL), undefined)
MULTICA_PUBLIC_URL := http://localhost:$(PORT)
endif
FRONTEND_PORT ?= 3000
FRONTEND_ORIGIN ?= http://localhost:$(FRONTEND_PORT)
MULTICA_APP_URL ?= $(FRONTEND_ORIGIN)
NEXT_PUBLIC_API_URL ?= http://localhost:$(PORT)
NEXT_PUBLIC_WS_URL ?= ws://localhost:$(PORT)/ws
MULTICA_SERVER_URL ?= ws://localhost:$(PORT)/ws
LOCAL_UPLOAD_BASE_URL ?= http://localhost:$(PORT)

export

MULTICA_ARGS ?= $(ARGS)

define REQUIRE_ENV
	@if [ ! -f "$(ENV_FILE)" ]; then \
		echo "Missing env file: $(ENV_FILE)"; \
		echo "Create .env from .env.example, or run 'make worktree-env' and use .env.worktree."; \
		exit 1; \
	fi
endef

# Bare `make` prints the local-workbench entry points.
.DEFAULT_GOAL := help

##@ Help

help: ## Show available make targets and common local workflows
	@awk 'BEGIN {FS = ":.*## "; printf "\nUsage:\n  make \033[36m<target>\033[0m\n\nQuick start:\n  \033[36mmake up\033[0m           Start the local API and Electron desktop (C can override)\n  \033[36mmake status\033[0m       Show what is running, and prove it is yours\n  \033[36mmake down\033[0m         Stop it again, keeping the database\n  \033[36mmake check\033[0m        Run the local-workbench verification pipeline\n\nCheckout modes:\n  Main checkout uses \033[36m.env\033[0m\n  Worktrees use \033[36m.env.worktree\033[0m (generate with \033[36mmake worktree-env\033[0m)\n\n"} \
		/^##@/ {printf "\n\033[1m%s\033[0m\n", substr($$0, 5); next} \
		/^[a-zA-Z0-9_.-]+:.*## / {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

makehelp: help ## Alias for `make help`

# ---------- Environments ----------
##@ Environments

# One verb per lifecycle step, shared by humans and agents. C= picks the
# components; ARGS= forwards anything else to the script.
#
#   make up                     local API + Electron desktop
#   make up C=api,desktop       spell out the default components
#   make up C=api               run only the SQLite API
#   make up ARGS=--ephemeral    agent-owned, expires, collected by `make gc`

up: ## Start this checkout's environment (C=api,daemon,desktop; default api,desktop)
	@bash scripts/dev-env.sh up $(if $(C),--components $(C)) $(ARGS)

down: ## Stop this environment's processes, keeping its database and profile
	@bash scripts/dev-env.sh down $(ARGS)

status: ## Show what is running for this environment, with proof of identity
	@bash scripts/dev-env.sh status $(ARGS)

list: ## List every registered development environment on this machine
	@bash scripts/dev-env.sh list $(ARGS)

destroy: ## Stop this environment, drop its database and profile, free its slot
	@bash scripts/dev-env.sh destroy $(ARGS)

gc: ## Collect environments whose directory is gone or whose TTL expired
	@bash scripts/dev-env.sh gc $(ARGS)

env-exec: ## Run a command with this environment's variables (ARGS="-- pnpm dev:desktop")
	@bash scripts/dev-env.sh exec $(ARGS)

# Single-component entry points. No database preflight: `up` has already proven
# the database is reachable before it launches anything, and repeating the
# check here would make each component slower to restart on its own.
# The commit ldflag is what makes /health's identity useful: without it every
# environment reports "unknown" and cannot prove which revision it is serving.
api-dev: ## Run only the Go backend for the current env file
	cd server && MULTICA_COMMIT=$(COMMIT) go run -ldflags "-X main.commit=$(COMMIT)" ./cmd/server

desktop-dev: ## Run only the Electron desktop app for the current env file
	pnpm dev:desktop

# ---------- One-click commands ----------
##@ One-click

setup: ## Install dependencies; SQLite initializes on first start
	$(REQUIRE_ENV)
	@echo "==> Using env file: $(ENV_FILE)"
	@echo "==> Installing dependencies..."
	pnpm install
	@echo ""
	@echo "✓ Setup complete. SQLite initializes on first local-server start."

start: ## Start the local SQLite backend and Electron desktop
	$(REQUIRE_ENV)
	@$(MAKE) up C=api,desktop ENV_FILE="$(ENV_FILE)"

stop: ## Stop local app processes for the current checkout
	$(REQUIRE_ENV)
	@$(MAKE) down ENV_FILE="$(ENV_FILE)"

check: ## Run typecheck, TS tests, Go tests, and the packaged Desktop build
	$(REQUIRE_ENV)
	@ENV_FILE="$(ENV_FILE)" bash scripts/check.sh

db-up: ## Initialize the local SQLite database
	@mkdir -p "$(dir $(MULTICA_SQLITE_PATH))"
	cd server && MULTICA_INIT_ONLY=1 MULTICA_SQLITE_PATH="$(MULTICA_SQLITE_PATH)" go run ./cmd/server

db-down: ## No-op: SQLite has no external database process
	@echo "SQLite is embedded; there is no database service to stop."

db-drop: ## Permanently delete the current env's SQLite database after confirmation
	$(REQUIRE_ENV)
	@status=0; MULTICA_SQLITE_PATH="$(MULTICA_SQLITE_PATH)" bash scripts/drop-sqlite.sh "$(ENV_FILE)" || status=$$?; \
		if [ "$$status" -eq 2 ]; then exit 0; fi; \
		exit "$$status"

db-reset: ## Delete and recreate the current env's SQLite database
	$(REQUIRE_ENV)
	@MULTICA_SQLITE_PATH="$(MULTICA_SQLITE_PATH)" bash scripts/drop-sqlite.sh "$(ENV_FILE)"
	@$(MAKE) db-up ENV_FILE="$(ENV_FILE)" MULTICA_SQLITE_PATH="$(MULTICA_SQLITE_PATH)"

worktree-env: ## Generate .env.worktree with local SQLite and unique app ports
	@bash scripts/init-worktree-env.sh .env.worktree

setup-main: ## Prepare the main checkout using .env
	@$(MAKE) setup ENV_FILE=$(MAIN_ENV_FILE)

start-main: ## Start the main checkout using .env
	@$(MAKE) start ENV_FILE=$(MAIN_ENV_FILE)

stop-main: ## Stop the main checkout processes defined by .env
	@$(MAKE) stop ENV_FILE=$(MAIN_ENV_FILE)

check-main: ## Run the full verification pipeline for the main checkout
	@ENV_FILE=$(MAIN_ENV_FILE) bash scripts/check.sh

setup-worktree: ## Ensure .env.worktree exists, then prepare this worktree
	@if [ ! -f "$(WORKTREE_ENV_FILE)" ]; then \
		echo "==> Generating $(WORKTREE_ENV_FILE) with unique ports..."; \
		bash scripts/init-worktree-env.sh $(WORKTREE_ENV_FILE); \
	else \
		echo "==> Using existing $(WORKTREE_ENV_FILE)"; \
	fi
	@$(MAKE) setup ENV_FILE=$(WORKTREE_ENV_FILE)

start-worktree: ## Start this worktree using .env.worktree
	@$(MAKE) start ENV_FILE=$(WORKTREE_ENV_FILE)

stop-worktree: ## Stop this worktree's backend and frontend processes
	@$(MAKE) stop ENV_FILE=$(WORKTREE_ENV_FILE)

check-worktree: ## Run the full verification pipeline for this worktree
	@ENV_FILE=$(WORKTREE_ENV_FILE) bash scripts/check.sh

remove-worktree: ## Delete a linked worktree's SQLite file, then remove it (WORKTREE=path)
	@bash scripts/remove-worktree.sh "$(WORKTREE)"

# ---------- Individual commands ----------
##@ Individual commands

dev: ## Bootstrap this checkout with SQLite and start local services
	@bash scripts/dev.sh

server: ## Run only the Go server for the current checkout
	$(REQUIRE_ENV)
	cd server && go run ./cmd/server

daemon: ## Restart the local Codex execution daemon
	@$(MAKE) multica MULTICA_ARGS="daemon restart --profile local"

cli: ## Run the multica CLI with ARGS or MULTICA_ARGS from source
	@$(MAKE) multica MULTICA_ARGS="$(MULTICA_ARGS)"

multica: ## Run the multica CLI entrypoint directly from the Go source tree
	cd server && go run -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)" ./cmd/multica $(MULTICA_ARGS)

VERSION ?= $(shell git describe --tags --match 'v[0-9]*' --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
# Windows will not execute an extensionless binary, so a source build there has
# to name its outputs the way the target platform expects — otherwise the CLI
# builds fine and then fails to re-exec itself as a daemon (#7255). GOOS reaches
# a build two ways: as an environment variable (`GOOS=windows make build`) and
# as a Make variable (`make build GOOS=windows`). The top-level `export` sends
# both forms to the recipe, so `go build` honors both and the suffix has to as
# well; `$(GOOS)` covers the Make-variable form, which a parse-time
# `go env GOOS` cannot see. Target-specific so only `build` pays for the probe:
# a global assignment runs `go env` on every target — `export` expands even a
# recursive one — which prints `go: Command not found` on frontend-only
# checkouts with no Go toolchain installed.
build: EXE = $(if $(filter windows,$(or $(GOOS),$(shell go env GOOS))),.exe,)
build: ## Build the local SQLite server and CLI into server/bin
	cd server && go build -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)" -o bin/server$(EXE) ./cmd/server
	cd server && go build -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)" -o bin/multica$(EXE) ./cmd/multica

test: ## Run Go tests; the local workbench tests use temporary SQLite files
	bash scripts/test-go.sh --race

# Database
##@ Database

migrate-up: ## Initialize or upgrade the local SQLite schema
	$(REQUIRE_ENV)
	@$(MAKE) db-up ENV_FILE="$(ENV_FILE)" MULTICA_SQLITE_PATH="$(MULTICA_SQLITE_PATH)"

migrate-down: ## SQLite schema changes are forward-only
	@echo "SQLite schema rollback is not supported; restore a database backup instead." >&2
	@exit 1

# Cleanup
##@ Cleanup

clean: ## Remove build caches, generated binaries, and temp files
	rm -rf server/bin server/tmp
	rm -rf apps/*/.next apps/*/.source apps/*/.expo
	rm -rf apps/*/out apps/*/dist apps/*/dist-electron packages/*/dist
	rm -rf .turbo apps/*/.turbo packages/*/.turbo
	rm -rf apps/*/*.tsbuildinfo packages/*/*.tsbuildinfo
	@echo "✓ Clean complete."
