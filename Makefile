.PHONY: gateway gateway-all build up down health test clean

# --- Gateway Binaries ---
GATEWAY_VERSION := 0.1.0
GATEWAY_DIR := cmd/tendril
DIST_DIR := cmd/tendril/dist

gateway: ## Build Gateway for current platform
	cd $(GATEWAY_DIR) && go build -ldflags="-s -w" -o tendril .

install: gateway ## Install tendril globally to ~/.local/bin
	mkdir -p ~/.local/bin
	mv $(GATEWAY_DIR)/tendril ~/.local/bin/tendril
	@echo "✅ Installed tendril to ~/.local/bin/tendril"
	@echo "Make sure ~/.local/bin is in your PATH."

gateway-all: ## Cross-compile Gateway for linux and macOS
	mkdir -p $(DIST_DIR)
	cd $(GATEWAY_DIR) && GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o dist/tendril-$(GATEWAY_VERSION)-linux-amd64 .
	cd $(GATEWAY_DIR) && GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o dist/tendril-$(GATEWAY_VERSION)-linux-arm64 .
	cd $(GATEWAY_DIR) && GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o dist/tendril-$(GATEWAY_VERSION)-darwin-amd64 .
	cd $(GATEWAY_DIR) && GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o dist/tendril-$(GATEWAY_VERSION)-darwin-arm64 .
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
	@echo "\nGateway:" && curl -s http://localhost:9090/health | python3 -m json.tool

# --- Development ---
test-core: ## Run Python tests
	PYTHONPATH=. python3 -m pytest tests/ -v

test-sprout: ## Run Go tests
	cd $(GATEWAY_DIR) && go test ./... -v

test-all: test-core test-sprout ## Run all tests

clean: ## Remove build artifacts
	rm -rf $(DIST_DIR) cmd/tendril/tendril
	docker compose down -v --remove-orphans

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
