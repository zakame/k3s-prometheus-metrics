# Makefile for github.com/zakame/k3s-prometheus-metrics

BINARY              := k3s-prometheus-metrics
CMD                 := ./cmd/$(BINARY)
BIN_DIR             := ./bin
CGO_ENABLED         ?= 0
CONTROLLER_GEN_VERSION := v0.21.0
RBAC_PATHS          := ./internal/controller/...
RBAC_OUT            := deploy/standard
ENVTEST_VERSION     := v0.24.1
ENVTEST_K8S_VERSION := 1.36.2

.DEFAULT_GOAL := help

.PHONY: help build test test-integration cover lint vet fmt manifests docker-build run clean

help: ## Show this help message
	@echo "Usage: make <target>"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

build: ## Compile the binary to ./bin/k3s-prometheus-metrics
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=$(CGO_ENABLED) go build -o $(BIN_DIR)/$(BINARY) $(CMD)

test: ## Run all tests
	go test -v -count=1 ./...

test-integration: ## Run envtest-backed integration tests (test/integration/, build tag integration)
	KUBEBUILDER_ASSETS="$$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION) use $(ENVTEST_K8S_VERSION) -p path)" \
		go test -tags integration -v -count=1 ./test/integration/...

cover: ## Run tests with coverage report
	go test -cover -coverpkg=./... -coverprofile=coverage.txt -covermode=atomic ./...
	go tool cover -func=coverage.txt

lint: ## Run golangci-lint if available, otherwise fall back to go vet
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not found, falling back to go vet"; \
		go vet ./...; \
	fi

vet: ## Run go vet
	go vet ./...

fmt: ## Check formatting (exits non-zero if files need formatting)
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

manifests: ## Generate deploy/standard/role.yaml from +kubebuilder:rbac markers
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION) \
		rbac:roleName=$(BINARY) \
		paths="$(RBAC_PATHS)" \
		output:rbac:artifacts:config=$(RBAC_OUT)

docker-build: ## Build a local container image (dev convenience; releases use goreleaser+ko)
	docker build -t $(BINARY):dev .

run: build ## Build and run the controller against the current kubeconfig context
	$(BIN_DIR)/$(BINARY)

clean: ## Remove build artifacts (./bin/, coverage.txt)
	rm -rf $(BIN_DIR) coverage.txt
