# Declared phony so the committed root `stem`/`tendril` binaries don't make
# these targets look "up to date" and get skipped (which broke `make install`).
.PHONY: stem install stem-all build up down health test-stem test-all clean check-all help

# --- Stem Binaries ---
STEM_VERSION := 0.2.0
STEM_DIR := cmd/stem
DIST_DIR := cmd/stem/dist

stem: ## Build the tendril binary (does not install it)
	@echo "🌱 Building the Stem for $$(go env GOOS)/$$(go env GOARCH)..."
	@cd $(STEM_DIR) && go build -ldflags="-s -w" -o tendril .
	@echo ""
	@echo "✅ Built: $(STEM_DIR)/tendril"
	@echo "   Nothing has been installed. Choose where it goes:"
	@echo ""
	@echo "   Governed install — hand it to the Stem's own user (see docs/GUIDE-INSTALL.md):"
	@echo "     sudo install -d -o tendril -g tendril -m 750 /home/tendril/.local/bin"
	@echo "     sudo install -o tendril -g tendril -m 750 $(STEM_DIR)/tendril /home/tendril/.local/bin/tendril"
	@echo ""
	@echo "   Single-user install — put it on your own PATH:"
	@echo "     make install"
	@echo ""

install: stem ## Build, then install tendril to your own ~/.local/bin
	@mkdir -p ~/.local/bin
	@mv $(STEM_DIR)/tendril ~/.local/bin/tendril
	@echo "✅ Installed: ~/.local/bin/tendril"
	@echo "   This is a SINGLE-USER install — the Stem will run as you."
	@echo "   Run 'tendril hardiness' to see what that means for the delegation boundary."
	@echo "   Ensure ~/.local/bin is on your PATH."

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

up: ## Start the Go Stem orchestrator locally
	go run ./cmd/stem serve

down: ## Stop all services
	docker compose down

health: ## Check service health
	@echo "Stem:" && curl -s http://localhost:8080/health | python3 -m json.tool

# --- Development ---
test-stem: ## Run Go tests in a sterile Docker container
	docker compose --profile test run --rm test-go

test-all: test-stem ## Run all tests

check-all: ## Full pre-merge gate: clean build + all tests (see .github/CONTRIBUTING.md / TESTING.md)
	$(MAKE) stem
	$(MAKE) test-all

clean: ## Remove build artifacts
	rm -rf $(DIST_DIR) cmd/stem/tendril
	docker compose down -v --remove-orphans

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
