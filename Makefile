.PHONY: sprout sprout-all build up down health test clean check-all

# --- Stem Binaries ---
STEM_VERSION := 0.1.0
STEM_DIR := cmd/stem
DIST_DIR := cmd/stem/dist

stem: ## Build Stem for current platform
	cd $(STEM_DIR) && go build -ldflags="-s -w" -o tendril .

install: stem ## Install tendril globally to ~/.local/bin
	mkdir -p ~/.local/bin
	mv $(STEM_DIR)/tendril ~/.local/bin/tendril
	@echo "✅ Installed tendril to ~/.local/bin/tendril"
	@echo "Make sure ~/.local/bin is in your PATH."

stem-all: ## Cross-compile Stem for linux and macOS
	mkdir -p $(DIST_DIR)
	cd $(STEM_DIR) && GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o dist/tendril-$(STEM_VERSION)-linux-amd64 .
	cd $(STEM_DIR) && GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o dist/tendril-$(STEM_VERSION)-linux-arm64 .
	cd $(STEM_DIR) && GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o dist/tendril-$(STEM_VERSION)-darwin-amd64 .
	cd $(STEM_DIR) && GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o dist/tendril-$(STEM_VERSION)-darwin-arm64 .
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
	@echo "Stem:" && curl -s http://localhost:8080/health | python3 -m json.tool
	@echo "\nTendril:" && curl -s http://localhost:9090/health | python3 -m json.tool

# --- Development ---
test-stem: ## Run Go tests in a sterile Docker container
	docker compose --profile test run --rm test-go

test-all: test-stem ## Run all tests

check-all: ## Full pre-merge gate: clean build + all tests (see CONTRIBUTING.md / TESTING.md)
	$(MAKE) stem
	$(MAKE) test-all

clean: ## Remove build artifacts
	rm -rf $(DIST_DIR) cmd/stem/tendril
	docker compose down -v --remove-orphans

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
