# Simple Makefile for building whatsapp-mcp with SQLite FTS5 support

BINARY      := bin/whatsapp-mcp
PKG         := ./cmd/whatsapp-mcp
TAGS        := sqlite_fts5
IMAGE_NAME  := ghcr.io/eddmann/whatsapp-mcp
VERSION     := $(shell git describe --tags --always --dirty)

.PHONY: build run clean tidy docker/build docker/build/multiplatform docker/run docker/push docker/login

##@ Build

build: ## Build the binary with CGO and FTS5 support
	@mkdir -p bin
	CGO_ENABLED=1 go build -tags "$(TAGS)" -o $(BINARY) $(PKG)

run: build ## Build and run the server
	./$(BINARY)

clean: ## Remove build artifacts
	rm -f $(BINARY)

tidy: ## Tidy go modules
	go mod tidy

format: ## Format Go source code
	go fmt ./...

##@ Docker

docker/build: ## Build Docker image locally
	docker build -t $(IMAGE_NAME):latest -t $(IMAGE_NAME):$(VERSION) .

docker/build/multiplatform: ## Build multi-platform Docker image (amd64, arm64)
	docker buildx build --platform linux/amd64,linux/arm64 -t $(IMAGE_NAME):latest -t $(IMAGE_NAME):$(VERSION) .

docker/run: ## Run the Docker container locally with volume mount
	docker run -it --rm \
		-v $(PWD)/store:/app/store \
		$(IMAGE_NAME):latest

docker/push: ## Push Docker image to registry (requires authentication)
	docker push $(IMAGE_NAME):latest
	docker push $(IMAGE_NAME):$(VERSION)

docker/login: ## Login to GitHub Container Registry
	echo $(GITHUB_TOKEN) | docker login ghcr.io -u $(GITHUB_USER) --password-stdin

##@ Info

version: ## Show current version
	@echo $(VERSION)

help: ## Show this help message
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_\-\/]+:.*?##/ { printf "  \033[36m%-30s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)


