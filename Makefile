# places — Go API + embedded SvelteKit frontend, shipped as one static binary.

BINARY   := places
IMAGE    := ghcr.io/epic9x/places:latest
API_DIR  := api
WEB_DIR  := web
# go:embed cannot reach outside its own package directory, so the SvelteKit
# build output is copied from web/build into api/webdist before `go build`.
EMBED_DIR := $(API_DIR)/webdist

.PHONY: help dev-api dev-web build build-web build-api docker clean test tidy check

help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

dev-api: ## Run the Go API on :8080 (CORS open to the SvelteKit dev server)
	cd $(API_DIR) && DATA_DIR=../data ADDR=:8080 CORS_ORIGIN=http://localhost:5173 go run .

dev-web: ## Run the SvelteKit dev server on :5173, proxying /api to :8080
	cd $(WEB_DIR) && npm run dev

build-web: ## Build the frontend and stage it for embedding
	cd $(WEB_DIR) && npm ci && npm run build
	rm -rf $(EMBED_DIR)
	mkdir -p $(EMBED_DIR)
	cp -r $(WEB_DIR)/build/. $(EMBED_DIR)/
	touch $(EMBED_DIR)/.gitkeep

build-api: ## Build the Go binary from whatever is already staged in api/webdist
	cd $(API_DIR) && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ../$(BINARY) .

build: build-web build-api ## Build the frontend, then the single binary
	@echo "built ./$(BINARY)"

docker: ## Build the container image
	docker build -t $(IMAGE) .

test: ## Run the Go tests
	cd $(API_DIR) && go test ./...

check: ## Vet the Go code and type-check the frontend
	cd $(API_DIR) && go vet ./...
	cd $(WEB_DIR) && npm run check

tidy: ## Tidy Go module dependencies
	cd $(API_DIR) && go mod tidy

clean: ## Remove build artifacts (leaves ./data alone)
	rm -rf $(BINARY) $(WEB_DIR)/build $(WEB_DIR)/.svelte-kit
	rm -rf $(EMBED_DIR)
	mkdir -p $(EMBED_DIR)
	touch $(EMBED_DIR)/.gitkeep
