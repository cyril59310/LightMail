# LightMail — development and operations shortcuts.
# Windows users: run from Git Bash or WSL.

VERSION ?= $(shell node -p "require('./frontend/package.json').version")
IMAGE   ?= lightmail
COMPOSE ?= docker compose

.PHONY: help frontend backend build test test-race lint docker docker-multiarch release up down logs ps secrets \
        backup verify-backup restore dev-mail dev clean

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'

frontend: ## Build the web client and embed it in the Go sources
	cd frontend && npm ci --no-audit --no-fund && npm run build
	rm -rf backend/internal/web/dist && mkdir -p backend/internal/web/dist
	cp -r frontend/dist/* backend/internal/web/dist/ && touch backend/internal/web/dist/.keep

backend: ## Build the server binary to bin/lightmail
	cd backend && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X lightmail/internal/api.Version=$(VERSION)" -o ../bin/lightmail ./cmd/lightmail

build: frontend backend ## Build everything locally

test: ## Run backend and frontend tests
	cd backend && go vet ./... && go test ./...
	cd frontend && npm run typecheck && npm test

test-race: ## Backend tests with the race detector (needs CGO; uses a container)
	docker run --rm -v "$(CURDIR)/backend:/src" -w /src -e CGO_ENABLED=1 golang:1.27 go test -race ./...

docker: ## Build the Docker image for the local platform
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

docker-multiarch: ## Build amd64 + arm64 images (needs a buildx builder; add --push to publish)
	docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) .

release: ## Publish a GitHub release (BUMP=patch|minor|major|X.Y.Z, default: current version)
	./scripts/release.sh $(BUMP)

secrets: ## Generate the master encryption key (once!)
	@mkdir -p secrets
	@if [ -f secrets/master_key ]; then echo "secrets/master_key already exists: not overwriting"; exit 1; fi
	@docker run --rm $(IMAGE):latest gen-key > secrets/master_key && chmod 600 secrets/master_key
	@echo "secrets/master_key created. Back it up somewhere safe: without it, stored credentials are lost."

up: ## Start (build if needed)
	$(COMPOSE) up -d --build

down: ## Stop
	$(COMPOSE) down

logs: ## Follow logs
	$(COMPOSE) logs -f lightmail

ps: ## Container status and health
	$(COMPOSE) ps

backup: ## Consistent backup into the data volume (/data/backups)
	$(COMPOSE) exec lightmail lightmail backup

verify-backup: ## make verify-backup FILE=/data/backups/xxx.tar.gz
	$(COMPOSE) exec lightmail lightmail verify-backup $(FILE)

restore: ## make restore FILE=/data/backups/xxx.tar.gz (stops the server)
	$(COMPOSE) stop lightmail
	$(COMPOSE) run --rm --no-deps lightmail restore $(FILE)
	$(COMPOSE) start lightmail

dev-mail: ## Local IMAPS/SMTPS test servers with generated messages
	cd backend && go run ./cmd/devmail -messages 2000 -accounts 2 -ca ../devmail-ca.pem -live 60s

dev: ## Run the backend locally (frontend dev server: cd frontend && npm run dev)
	cd backend && LIGHTMAIL_DATA_DIR=../data LIGHTMAIL_LOG_FORMAT=text \
	  LIGHTMAIL_MASTER_KEY=$${LIGHTMAIL_MASTER_KEY:?export LIGHTMAIL_MASTER_KEY first} \
	  LIGHTMAIL_PUBLIC_URL=http://localhost:5173 go run ./cmd/lightmail serve

clean: ## Remove build outputs
	rm -rf bin frontend/dist backend/internal/web/dist/assets backend/internal/web/dist/index.html backend/internal/web/dist/favicon.svg
