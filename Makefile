.PHONY: cli cli-all build up down health test clean

# --- CLI Binaries ---
CLI_VERSION := 0.1.0
CLI_DIR := cli
DIST_DIR := cli/dist

cli: ## Build CLI for current platform
	cd $(CLI_DIR) && go build -ldflags="-s -w" -o tendril-cli .

cli-all: ## Cross-compile CLI for linux and macOS
	mkdir -p $(DIST_DIR)
	cd $(CLI_DIR) && GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o dist/tendril-$(CLI_VERSION)-linux-amd64 .
	cd $(CLI_DIR) && GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o dist/tendril-$(CLI_VERSION)-linux-arm64 .
	cd $(CLI_DIR) && GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o dist/tendril-$(CLI_VERSION)-darwin-amd64 .
	cd $(CLI_DIR) && GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o dist/tendril-$(CLI_VERSION)-darwin-arm64 .
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
test: ## Run Python tests
	cd src && python -m pytest ../tests/ -v

clean: ## Remove build artifacts
	rm -rf $(DIST_DIR) cli/tendril-cli
	docker compose down -v --remove-orphans

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
