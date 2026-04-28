.PHONY: help build run test clean docker-build setup deploy k8s-deploy k8s-delete k8s-logs k8s-status

# Default target
.DEFAULT_GOAL := help

# Variables
BINARY_NAME=research-agent-telemetry
DOCKER_IMAGE=research-agent-telemetry:latest
K8S_NAMESPACE=truvag3-examples

## help: Show this help message
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' | sed -e 's/^/ /'

## build: Build the Go binary
build:
	@echo "🔨 Building $(BINARY_NAME)..."
	@go build -o $(BINARY_NAME) .
	@echo "✅ Build complete: ./$(BINARY_NAME)"

## run: Run the agent locally (requires Redis)
run: build
	@echo "🚀 Starting $(BINARY_NAME)..."
	@echo "📋 Make sure Redis is running: docker run -p 6379:6379 redis:7-alpine"
	@./$(BINARY_NAME)

## test: Run tests
test:
	@echo "🧪 Running tests..."
	@go test -v ./...

## clean: Clean build artifacts
clean:
	@echo "🧹 Cleaning build artifacts..."
	@rm -f $(BINARY_NAME)
	@rm -f *.test
	@rm -f *.out
	@echo "✅ Clean complete"

## docker-build: Build Docker image
docker-build:
	@echo "🐳 Building Docker image $(DOCKER_IMAGE)..."
	@docker build -t $(DOCKER_IMAGE) .
	@echo "✅ Docker image built: $(DOCKER_IMAGE)"

## setup: Initial setup (install dependencies, create .env)
setup:
	@echo "📦 Setting up environment..."
	@if [ ! -f .env ]; then \
		echo "📝 Creating .env from .env.example..."; \
		cp .env.example .env; \
		echo "⚠️  Please edit .env and add your API keys"; \
	else \
		echo "ℹ️  .env already exists"; \
	fi
	@echo "📥 Downloading Go dependencies..."
	@go mod download
	@echo "✅ Setup complete"

## deploy: Deploy to local Kind cluster
deploy: docker-build
	@echo "☸️  Deploying to Kind cluster..."
	@kind load docker-image $(DOCKER_IMAGE) --name truvag3-demo || echo "⚠️  Kind cluster 'truvag3-demo' not found. Run setup-kind-demo.sh first"
	@kubectl apply -f k8-deployment.yaml
	@echo "✅ Deployment complete"

## k8s-deploy: Deploy to Kubernetes
k8s-deploy:
	@echo "☸️  Deploying to Kubernetes..."
	@kubectl create namespace $(K8S_NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -
	@kubectl apply -f k8-deployment.yaml
	@echo "✅ Deployed to namespace: $(K8S_NAMESPACE)"
	@echo "📋 Check status with: kubectl get pods -n $(K8S_NAMESPACE)"

## k8s-delete: Delete from Kubernetes
k8s-delete:
	@echo "🗑️  Deleting from Kubernetes..."
	@kubectl delete -f k8-deployment.yaml --ignore-not-found=true
	@echo "✅ Deleted"

## k8s-logs: View Kubernetes logs
k8s-logs:
	@kubectl logs -f -l app=research-agent-telemetry -n $(K8S_NAMESPACE)

## k8s-status: Check Kubernetes deployment status
k8s-status:
	@echo "📊 Deployment Status:"
	@kubectl get deployments -n $(K8S_NAMESPACE) -l app=research-agent-telemetry
	@echo ""
	@echo "📦 Pods:"
	@kubectl get pods -n $(K8S_NAMESPACE) -l app=research-agent-telemetry
	@echo ""
	@echo "🌐 Services:"
	@kubectl get services -n $(K8S_NAMESPACE) -l app=research-agent-telemetry

## dev: Run in development mode with live reload (requires air)
dev:
	@if command -v air > /dev/null; then \
		air; \
	else \
		echo "❌ 'air' not found. Install it with: go install github.com/cosmtrek/air@latest"; \
		exit 1; \
	fi

## fmt: Format code
fmt:
	@echo "🎨 Formatting code..."
	@go fmt ./...
	@echo "✅ Code formatted"

## vet: Run go vet
vet:
	@echo "🔍 Running go vet..."
	@go vet ./...
	@echo "✅ Vet complete"

## lint: Run golangci-lint (if installed)
lint:
	@if command -v golangci-lint > /dev/null; then \
		echo "🔍 Running golangci-lint..."; \
		golangci-lint run; \
		echo "✅ Lint complete"; \
	else \
		echo "⚠️  golangci-lint not installed. Install from https://golangci-lint.run/"; \
	fi

## check: Run all checks (fmt, vet, lint, test)
check: fmt vet lint test
	@echo "✅ All checks passed"
