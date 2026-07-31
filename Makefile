# Define the Go binary and output directory
GO ?= go
OUTPUT_DIR ?= ./bin
PROJECT_NAME ?= gitlab-achievements
MAIN_FILE ?= .
DOCKERFILE ?= Containerfile
DOCKER_ENGINE ?= podman
GO_BUILD_FLAGS ?= -buildvcs=true
CHART_DIR ?= chart
# Placeholder values for the settings the chart requires, so it can be
# rendered without a real instance behind it.
CHART_TEST_VALUES ?= --set config.gitlabUrl=https://gitlab.example.com \
	--set config.achievementsNamespace=achievements \
	--set config.publicUrl=https://achievements.example.com \
	--set secrets.existingSecret=credentials

# Default target
.DEFAULT_GOAL := build

# Download dependencies
deps:
	@echo "Downloading dependencies..."
	$(GO) mod download

# Build target
build: deps
	@echo "Building the binary..."
	$(GO) build $(GO_BUILD_FLAGS) -o $(OUTPUT_DIR)/$(PROJECT_NAME) $(MAIN_FILE)

# Lint target
lint: deps
	@command -v golangci-lint >/dev/null 2>&1 || { echo "Installing golangci-lint..."; go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2; }
	@echo "Running golangci-lint..."
	golangci-lint run ./...

dependency-check:
	@echo "Running dependency-check..."
	dependency-check --nvdApiKey $(NVD_API_KEY) --scan ./ --format ALL --out dependency-check/ --enableExperimental

# Test target
test: deps
	@command -v gotestsum >/dev/null 2>&1 || { echo "Installing gotestsum..."; go install gotest.tools/gotestsum@v1.13.0; }
	@mkdir -p codequality
	gotestsum --junitfile codequality/unit-tests.xml --format-icons octicons -- -coverprofile=codequality/coverage.out -covermode=atomic ./...
	@echo "Coverage report generated: codequality/coverage.html"


# Docker target
package:
	@echo "Building Docker image..."
	$(DOCKER_ENGINE) build -t $(PROJECT_NAME):dev -f $(DOCKERFILE) .

# Helm chart targets. chart-lint renders every optional piece too, since a
# template that only breaks when an Ingress or the backfill Job is enabled
# still breaks somebody's install.
helm/lint:
	@echo "Linting the Helm chart..."
	helm lint $(CHART_DIR) $(CHART_TEST_VALUES)
	@helm template release $(CHART_DIR) $(CHART_TEST_VALUES) \
		--set ingress.enabled=true \
		--set config.backfill.mode=off \
		--set backfillJob.enabled=true > /dev/null
	@echo "Chart renders."

helm/template:
	@echo "Packaging the Helm chart..."
	helm template gitlab-achievements --debug $(CHART_DIR)

# Phony targets
.PHONY: deps build lint dependency-check test package helm/lint helm/package
