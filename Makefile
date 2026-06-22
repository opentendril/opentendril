.PHONY: sprout sprout-all build up down health test clean

# --- Sprout Binaries ---
SPROUT_VERSION := 0.1.0
SPROUT_DIR := cmd/stem
DIST_DIR := cmd/stem/dist

sprout: ## Build Sprout for current platform
	cd $(SPROUT_DIR) && go build -ldflags="-s -w" -o tendril .

install: sprout ## Install tendril globally to ~/.local/bin
	mkdir -p ~/.local/bin
	mv $(SPROUT_DIR)/tendril ~/.local/bin/tendril
	@echo "✅ Installed tendril to ~/.local/bin/tendril"
	@echo "Make sure ~/.local/bin is in your PATH."

sprout-all: ## Cross-compile Sprout for linux and macOS
	mkdir -p $(DIST_DIR)
	cd $(SPROUT_DIR) && GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o dist/tendril-$(SPROUT_VERSION)-linux-amd64 .
	cd $(SPROUT_DIR) && GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o dist/tendril-$(SPROUT_VERSION)-linux-arm64 .
	cd $(SPROUT_DIR) && GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o dist/tendril-$(SPROUT_VERSION)-darwin-amd64 .
	cd $(SPROUT_DIR) && GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o dist/tendril-$(SPROUT_VERSION)-darwin-arm64 .
	@echo "✅ Binaries in $(DIST_DIR)/"
	@ls -lh $(DIST_DIR)/

# --- Docker ---
build: ## Build all containers
	docker compose build

up: ## Start all services
	docker compose up --build

down: ## Stop all services
	docker compose down

health: ## Check service health
	@echo "Brain:" && curl -s http://localhost:8080/health | python3 -m json.tool
	@echo "\nSprout:" && curl -s http://localhost:9090/health | python3 -m json.tool

# --- Development ---
test-core: ## Run Python tests
	cd tendrils/python && PYTHONPATH=. python3 -m pytest tests/ -v

test-sprout: ## Run Go tests
	cd $(SPROUT_DIR) && go test ./... -v

test-all: test-core test-sprout ## Run all tests

clean: ## Remove build artifacts
	rm -rf $(DIST_DIR) cmd/stem/tendril
	docker compose down -v --remove-orphans

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
