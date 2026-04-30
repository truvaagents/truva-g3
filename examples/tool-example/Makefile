# Makefile for TruvaG3 Tool Example
# Easy deployment to local Kind cluster

.PHONY: all help setup deploy test clean logs status

# Variables
CLUSTER_NAME := truvag3-demo
NAMESPACE := truvag3-examples
APP_NAME := weather-tool
IMAGE_NAME := $(APP_NAME):latest

# Colors for output
RED := \033[0;31m
GREEN := \033[0;32m
YELLOW := \033[1;33m
BLUE := \033[0;34m
RESET := \033[0m

# Include .env file if it exists
-include .env
export

help: ## Show this help message
	@echo "$(BLUE)TruvaG3 Weather Tool Example - Local Development$(RESET)"
	@echo ""
	@echo "$(YELLOW)Usage:$(RESET)"
	@echo "  make setup    - Complete setup (Kind cluster + dependencies)"
	@echo "  make deploy   - Deploy the weather tool"
	@echo "  make test     - Run tests against deployed tool"
	@echo "  make logs     - View tool logs"
	@echo "  make status   - Check deployment status"
	@echo "  make clean    - Clean up everything"
	@echo ""
	@echo "$(YELLOW)Quick Start:$(RESET)"
	@echo "  1. (Optional) Copy .env.example to .env and add your Weather API key"
	@echo "  2. Run: make setup deploy"
	@echo "  3. Test: make test"
	@echo ""
	@echo "$(YELLOW)Note:$(RESET) Weather tool works with mock data without API key"
	@echo ""

all: setup deploy test ## Run complete setup, deploy, and test

check-prerequisites: ## Check if required tools are installed
	@echo "$(BLUE)Checking prerequisites...$(RESET)"
	@which docker > /dev/null || (echo "$(RED)Docker is not installed$(RESET)" && exit 1)
	@which kind > /dev/null || (echo "$(RED)Kind is not installed. Install from: https://kind.sigs.k8s.io/$(RESET)" && exit 1)
	@which kubectl > /dev/null || (echo "$(RED)kubectl is not installed$(RESET)" && exit 1)
	@echo "$(GREEN)✓ All prerequisites installed$(RESET)"

setup: check-prerequisites ## Create Kind cluster and install dependencies
	@echo "$(BLUE)Setting up Kind cluster...$(RESET)"
	@if kind get clusters | grep -q $(CLUSTER_NAME); then \
		echo "$(YELLOW)Cluster $(CLUSTER_NAME) already exists$(RESET)"; \
	else \
		echo "Creating Kind cluster..."; \
		kind create cluster --name $(CLUSTER_NAME) --config kind-config.yaml || exit 1; \
		echo "$(GREEN)✓ Kind cluster created$(RESET)"; \
	fi
	@kubectl config use-context kind-$(CLUSTER_NAME)
	@echo "$(BLUE)Creating namespace...$(RESET)"
	@kubectl create namespace $(NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -
	@echo "$(GREEN)✓ Namespace created$(RESET)"
	@echo "$(BLUE)Checking for Redis...$(RESET)"
	@$(MAKE) install-redis
	@echo "$(BLUE)Creating secrets...$(RESET)"
	@$(MAKE) create-secrets
	@echo "$(GREEN)✓ Setup complete!$(RESET)"

install-redis: ## Install Redis in the cluster if not already present
	@if kubectl get deployment redis -n default > /dev/null 2>&1; then \
		echo "$(GREEN)✓ Redis already running in default namespace$(RESET)"; \
	elif kubectl get deployment redis -n $(NAMESPACE) > /dev/null 2>&1; then \
		echo "$(GREEN)✓ Redis already running in $(NAMESPACE) namespace$(RESET)"; \
	else \
		echo "$(YELLOW)No Redis found, installing in default namespace...$(RESET)"; \
		kubectl apply -f redis-deployment.yaml -n default; \
		echo "Waiting for Redis to be ready..."; \
		kubectl wait --for=condition=available --timeout=60s deployment/redis -n default || true; \
		echo "$(GREEN)✓ Redis installed$(RESET)"; \
	fi

create-secrets: ## Create Weather API key secret
	@if [ -z "$$WEATHER_API_KEY" ]; then \
		echo "$(YELLOW)⚠ WEATHER_API_KEY not found in environment or .env file$(RESET)"; \
		echo "$(YELLOW)  Weather tool will use mock data.$(RESET)"; \
		echo "$(YELLOW)  For real weather data:$(RESET)"; \
		echo "$(YELLOW)  1. Get API key from: https://www.weatherapi.com/$(RESET)"; \
		echo "$(YELLOW)  2. Copy .env.example to .env$(RESET)"; \
		echo "$(YELLOW)  3. Add your WEATHER_API_KEY to .env$(RESET)"; \
		echo "$(YELLOW)  4. Run: source .env && make create-secrets$(RESET)"; \
		kubectl create secret generic weather-tool-secrets \
			--from-literal=WEATHER_API_KEY="mock-key" \
			-n $(NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -; \
	else \
		kubectl create secret generic weather-tool-secrets \
			--from-literal=WEATHER_API_KEY="$$WEATHER_API_KEY" \
			-n $(NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -; \
		echo "$(GREEN)✓ Weather API key secret created$(RESET)"; \
	fi

build: ## Build the Docker image (standalone - fetches v0.6.4 from GitHub)
	@echo "$(BLUE)Building Docker image (standalone mode)...$(RESET)"
	@echo "$(YELLOW)Note: Building independently, fetching truvag3 v0.6.4 from GitHub$(RESET)"
	@docker build -t $(IMAGE_NAME) .
	@echo "$(GREEN)✓ Docker image built (standalone)$(RESET)"

load-image: build ## Load Docker image into Kind
	@echo "$(BLUE)Loading image into Kind cluster...$(RESET)"
	@kind load docker-image $(IMAGE_NAME) --name $(CLUSTER_NAME)
	@echo "$(GREEN)✓ Image loaded into cluster$(RESET)"

deploy: load-image ## Deploy the tool to Kind cluster
	@echo "$(BLUE)Deploying weather tool...$(RESET)"
	@kubectl apply -f k8-deployment.yaml
	@echo "Waiting for deployment to be ready..."
	@kubectl wait --for=condition=available --timeout=120s deployment/$(APP_NAME) -n $(NAMESPACE) || \
		(echo "$(RED)Deployment failed. Check logs with: make logs$(RESET)" && exit 1)
	@echo "$(GREEN)✓ Weather tool deployed successfully!$(RESET)"
	@echo ""
	@$(MAKE) status

test: ## Test the deployed tool
	@echo "$(BLUE)Testing weather tool endpoints...$(RESET)"
	@echo ""
	@echo "1. Testing health endpoint..."
	@kubectl port-forward -n $(NAMESPACE) svc/weather-service 8080:80 > /dev/null 2>&1 & \
		sleep 2; \
		curl -s http://localhost:8080/health | jq '.' 2>/dev/null || echo "Response: $$(curl -s http://localhost:8080/health)"; \
		pkill -f "port-forward.*weather-service" || true
	@echo ""
	@echo "2. Testing capabilities endpoint..."
	@kubectl port-forward -n $(NAMESPACE) svc/weather-service 8080:80 > /dev/null 2>&1 & \
		sleep 2; \
		curl -s http://localhost:8080/api/capabilities | jq '.' 2>/dev/null || echo "Response: $$(curl -s http://localhost:8080/api/capabilities)"; \
		pkill -f "port-forward.*weather-service" || true
	@echo ""
	@echo "3. Testing current weather endpoint..."
	@kubectl port-forward -n $(NAMESPACE) svc/weather-service 8080:80 > /dev/null 2>&1 & \
		sleep 2; \
		curl -s -X POST http://localhost:8080/api/capabilities/current_weather \
			-H "Content-Type: application/json" \
			-d '{"location":"London","units":"metric"}' | jq '.' 2>/dev/null || \
		echo "Response: $$(curl -s -X POST http://localhost:8080/api/capabilities/current_weather -H 'Content-Type: application/json' -d '{\"location\":\"London\"}')"; \
		pkill -f "port-forward.*weather-service" || true
	@echo ""
	@echo "4. Testing schema endpoint (Phase 3)..."
	@kubectl port-forward -n $(NAMESPACE) svc/weather-service 8080:80 > /dev/null 2>&1 & \
		sleep 2; \
		curl -s http://localhost:8080/api/capabilities/current_weather/schema | jq '.' 2>/dev/null || \
		echo "Response: $$(curl -s http://localhost:8080/api/capabilities/current_weather/schema)"; \
		pkill -f "port-forward.*weather-service" || true
	@echo ""
	@echo "$(GREEN)✓ Tests complete$(RESET)"

logs: ## View tool logs
	@echo "$(BLUE)Weather tool logs (last 50 lines):$(RESET)"
	@kubectl logs -n $(NAMESPACE) -l app=$(APP_NAME) --tail=50 -f

status: ## Check deployment status
	@echo "$(BLUE)Deployment Status:$(RESET)"
	@echo ""
	@echo "Pods:"
	@kubectl get pods -n $(NAMESPACE) -l app=$(APP_NAME) -o wide
	@echo ""
	@echo "Services:"
	@kubectl get svc -n $(NAMESPACE) -l app=$(APP_NAME)
	@echo ""
	@echo "Deployments:"
	@kubectl get deployment -n $(NAMESPACE) $(APP_NAME)
	@echo ""
	@echo "$(YELLOW)Access the weather tool:$(RESET)"
	@echo "  kubectl port-forward -n $(NAMESPACE) svc/weather-service 8080:80"
	@echo "  Then visit: http://localhost:8080/health"
	@echo "  Or test:    curl http://localhost:8080/api/capabilities"

clean: ## Delete Kind cluster and clean up
	@echo "$(YELLOW)Cleaning up...$(RESET)"
	@kind delete cluster --name $(CLUSTER_NAME) 2>/dev/null || true
	@docker rmi $(IMAGE_NAME) 2>/dev/null || true
	@echo "$(GREEN)✓ Cleanup complete$(RESET)"

port-forward: ## Start port forwarding to access the tool
	@echo "$(BLUE)Starting port forward...$(RESET)"
	@echo "Weather tool will be available at: http://localhost:8080"
	@echo "Press Ctrl+C to stop"
	@kubectl port-forward -n $(NAMESPACE) svc/weather-service 8080:80

debug: ## Debug information
	@echo "$(BLUE)Debug Information:$(RESET)"
	@echo ""
	@echo "Pod Status:"
	@kubectl describe pod -n $(NAMESPACE) -l app=$(APP_NAME) | grep -A 5 "Status:"
	@echo ""
	@echo "Recent Events:"
	@kubectl get events -n $(NAMESPACE) --sort-by='.lastTimestamp' | tail -10
	@echo ""
	@echo "Pod Logs (errors only):"
	@kubectl logs -n $(NAMESPACE) -l app=$(APP_NAME) --tail=20 | grep -i error || echo "No errors found"

# Development helpers
dev-test: ## Quick test without full deployment (standalone mode)
	@echo "$(BLUE)Running standalone build test...$(RESET)"
	@echo "$(YELLOW)Note: Building independently, fetching truvag3 v0.6.4 from GitHub$(RESET)"
	@env GOWORK=off GOPROXY=direct GONOSUMDB='github.com/truvaagents/truva-g3/*' \
		go build -v -o /tmp/weather-tool-test . && \
		echo "$(GREEN)✓ Tool builds successfully (standalone)$(RESET)" && \
		rm /tmp/weather-tool-test

check-cluster: ## Check if cluster is running
	@if kind get clusters | grep -q $(CLUSTER_NAME); then \
		echo "$(GREEN)✓ Cluster $(CLUSTER_NAME) is running$(RESET)"; \
		kubectl cluster-info --context kind-$(CLUSTER_NAME); \
	else \
		echo "$(RED)✗ Cluster $(CLUSTER_NAME) not found$(RESET)"; \
		echo "Run: make setup"; \
		exit 1; \
	fi
