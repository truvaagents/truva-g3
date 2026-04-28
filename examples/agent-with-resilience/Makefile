.PHONY: build run test clean docker-build docker-run k8s-deploy help

# Variables
APP_NAME := research-agent-resilience
DOCKER_IMAGE := truvag3/$(APP_NAME)
DOCKER_TAG := latest
PORT := 8093

# Default target
all: build

# Build the application
build:
	@echo "Building $(APP_NAME)..."
	go build -o $(APP_NAME) .

# Run locally (requires REDIS_URL to be set)
run: build
	@echo "Starting $(APP_NAME) on port $(PORT)..."
	@if [ -z "$$REDIS_URL" ]; then \
		echo "Warning: REDIS_URL not set, using default"; \
		export REDIS_URL="redis://localhost:6379"; \
	fi
	./$(APP_NAME)

# Run with environment file
run-env: build
	@echo "Starting $(APP_NAME) with .env file..."
	@if [ -f .env ]; then \
		export $$(cat .env | xargs) && ./$(APP_NAME); \
	else \
		echo "Error: .env file not found. Copy .env.example to .env"; \
		exit 1; \
	fi

# Run tests
test:
	@echo "Running tests..."
	go test -v ./...

# Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -f $(APP_NAME)
	go clean

# Download dependencies
deps:
	@echo "Downloading dependencies..."
	go mod download
	go mod tidy

# Build Docker image
docker-build:
	@echo "Building Docker image..."
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

# Run Docker container
docker-run: docker-build
	@echo "Running Docker container..."
	docker run -p $(PORT):$(PORT) \
		-e REDIS_URL=redis://host.docker.internal:6379 \
		-e PORT=$(PORT) \
		$(DOCKER_IMAGE):$(DOCKER_TAG)

# Deploy to Kubernetes (generic)
k8s-deploy:
	@echo "Deploying to Kubernetes..."
	kubectl apply -f k8-deployment.yaml

# Remove from Kubernetes
k8s-delete:
	@echo "Removing from Kubernetes..."
	kubectl delete -f k8-deployment.yaml

# Deploy to Kind cluster (truvag3-demo)
kind-deploy:
	@echo "Deploying to Kind cluster..."
	./deploy.sh deploy

# Remove from Kind cluster
kind-delete:
	@echo "Removing from Kind cluster..."
	./deploy.sh cleanup

# Show deployment status in Kind
kind-status:
	./deploy.sh status

# Stream logs from Kind deployment
kind-logs:
	./deploy.sh logs

# Show help
help:
	@echo "Available targets:"
	@echo "  build        - Build the application"
	@echo "  run          - Build and run locally"
	@echo "  run-env      - Run with .env file"
	@echo "  test         - Run tests"
	@echo "  clean        - Clean build artifacts"
	@echo "  deps         - Download dependencies"
	@echo "  docker-build - Build Docker image"
	@echo "  docker-run   - Run in Docker"
	@echo "  k8s-deploy   - Deploy to Kubernetes (generic)"
	@echo "  k8s-delete   - Remove from Kubernetes"
	@echo "  kind-deploy  - Deploy to Kind cluster (truvag3-demo)"
	@echo "  kind-delete  - Remove from Kind cluster"
	@echo "  kind-status  - Show deployment status"
	@echo "  kind-logs    - Stream pod logs"
	@echo "  help         - Show this help"

# Development helpers
.PHONY: fmt lint

fmt:
	@echo "Formatting code..."
	go fmt ./...

lint:
	@echo "Linting code..."
	golangci-lint run ./...
