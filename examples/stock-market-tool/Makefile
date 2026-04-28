# Makefile for Truva-G3 Stock Market Tool
# Easy deployment to local Kind cluster

.PHONY: all help setup deploy test clean logs status

# Variables
CLUSTER_NAME := truvag3-demo-$(shell whoami)
NAMESPACE := truvag3-examples
APP_NAME := stock-tool
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
	@echo "$(BLUE)Truva-G3 Stock Market Tool - Local Development$(RESET)"
	@echo ""
	@echo "$(YELLOW)Usage:$(RESET)"
	@echo "  make setup    - Complete setup (Kind cluster + dependencies)"
	@echo "  make deploy   - Deploy the stock market tool"
	@echo "  make test     - Run tests against deployed tool"
	@echo "  make logs     - View tool logs"
	@echo "  make status   - Check deployment status"
	@echo "  make clean    - Clean up everything"
	@echo ""
	@echo "$(YELLOW)Quick Start:$(RESET)"
	@echo "  1. Copy .env.example to .env and add your Finnhub API key"
	@echo "  2. Get API key from: https://finnhub.io/register"
	@echo "  3. Run: source .env && make setup deploy"
	@echo "  4. Test: make test"
	@echo ""
	@echo "$(YELLOW)Note:$(RESET) Stock tool works with mock data without API key"
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
		kind create cluster --name $(CLUSTER_NAME) || exit 1; \
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
		echo "$(YELLOW)Note: Use weather-tool's redis-deployment.yaml or your own Redis setup$(RESET)"; \
		echo "$(RED)Please ensure Redis is running before deploying$(RESET)"; \
	fi

create-secrets: ## Create Finnhub API key secret
	@if [ -z "$$FINNHUB_API_KEY" ]; then \
		echo "$(YELLOW)⚠ FINNHUB_API_KEY not found in environment or .env file$(RESET)"; \
		echo "$(YELLOW)  Stock tool will use mock data.$(RESET)"; \
		echo "$(YELLOW)  For real stock market data:$(RESET)"; \
		echo "$(YELLOW)  1. Get FREE API key from: https://finnhub.io/register$(RESET)"; \
		echo "$(YELLOW)  2. Copy .env.example to .env$(RESET)"; \
		echo "$(YELLOW)  3. Add your FINNHUB_API_KEY to .env$(RESET)"; \
		echo "$(YELLOW)  4. Run: source .env && make create-secrets$(RESET)"; \
		kubectl create secret generic stock-tool-secrets \
			--from-literal=FINNHUB_API_KEY="mock-key" \
			-n $(NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -; \
	else \
		kubectl create secret generic stock-tool-secrets \
			--from-literal=FINNHUB_API_KEY="$$FINNHUB_API_KEY" \
			-n $(NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -; \
		echo "$(GREEN)✓ Finnhub API key secret created$(RESET)"; \
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
	@echo "$(BLUE)Deploying stock market tool...$(RESET)"
	@kubectl apply -f k8-deployment.yaml
	@echo "Waiting for deployment to be ready..."
	@kubectl wait --for=condition=available --timeout=120s deployment/$(APP_NAME) -n $(NAMESPACE) || \
		(echo "$(RED)Deployment failed. Check logs with: make logs$(RESET)" && exit 1)
	@echo "$(GREEN)✓ Stock market tool deployed successfully!$(RESET)"
	@echo ""
	@$(MAKE) status

test: ## Test the deployed tool
	@echo "$(BLUE)Testing stock market tool endpoints...$(RESET)"
	@echo ""
	@echo "1. Testing health endpoint..."
	@kubectl port-forward -n $(NAMESPACE) svc/stock-service 8082:80 > /dev/null 2>&1 & \
		sleep 2; \
		curl -s http://localhost:8082/health | jq '.' 2>/dev/null || echo "Response: $$(curl -s http://localhost:8082/health)"; \
		pkill -f "port-forward.*stock-service" || true
	@echo ""
	@echo "2. Testing capabilities endpoint..."
	@kubectl port-forward -n $(NAMESPACE) svc/stock-service 8082:80 > /dev/null 2>&1 & \
		sleep 2; \
		curl -s http://localhost:8082/api/capabilities | jq '.' 2>/dev/null || echo "Response: $$(curl -s http://localhost:8082/api/capabilities)"; \
		pkill -f "port-forward.*stock-service" || true
	@echo ""
	@echo "3. Testing stock quote endpoint..."
	@kubectl port-forward -n $(NAMESPACE) svc/stock-service 8082:80 > /dev/null 2>&1 & \
		sleep 2; \
		curl -s -X POST http://localhost:8082/api/capabilities/stock_quote \
			-H "Content-Type: application/json" \
			-d '{"symbol":"AAPL"}' | jq '.' 2>/dev/null || \
		echo "Response: $$(curl -s -X POST http://localhost:8082/api/capabilities/stock_quote -H 'Content-Type: application/json' -d '{\"symbol\":\"AAPL\"}')"; \
		pkill -f "port-forward.*stock-service" || true
	@echo ""
	@echo "4. Testing company profile endpoint..."
	@kubectl port-forward -n $(NAMESPACE) svc/stock-service 8082:80 > /dev/null 2>&1 & \
		sleep 2; \
		curl -s -X POST http://localhost:8082/api/capabilities/company_profile \
			-H "Content-Type: application/json" \
			-d '{"symbol":"TSLA"}' | jq '.' 2>/dev/null || \
		echo "Response: $$(curl -s -X POST http://localhost:8082/api/capabilities/company_profile -H 'Content-Type: application/json' -d '{\"symbol\":\"TSLA\"}')"; \
		pkill -f "port-forward.*stock-service" || true
	@echo ""
	@echo "5. Testing schema endpoint (Phase 3)..."
	@kubectl port-forward -n $(NAMESPACE) svc/stock-service 8082:80 > /dev/null 2>&1 & \
		sleep 2; \
		curl -s http://localhost:8082/api/capabilities/stock_quote/schema | jq '.' 2>/dev/null || \
		echo "Response: $$(curl -s http://localhost:8082/api/capabilities/stock_quote/schema)"; \
		pkill -f "port-forward.*stock-service" || true
	@echo ""
	@echo "$(GREEN)✓ Tests complete$(RESET)"

logs: ## View tool logs
	@echo "$(BLUE)Stock tool logs (last 50 lines):$(RESET)"
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
	@echo "$(YELLOW)Access the stock market tool:$(RESET)"
	@echo "  kubectl port-forward -n $(NAMESPACE) svc/stock-service 8082:80"
	@echo "  Then visit: http://localhost:8082/health"
	@echo "  Or test:    curl http://localhost:8082/api/capabilities"

clean: ## Delete Kind cluster and clean up
	@echo "$(YELLOW)Cleaning up...$(RESET)"
	@kind delete cluster --name $(CLUSTER_NAME) 2>/dev/null || true
	@docker rmi $(IMAGE_NAME) 2>/dev/null || true
	@echo "$(GREEN)✓ Cleanup complete$(RESET)"

port-forward: ## Start port forwarding to access the tool
	@echo "$(BLUE)Starting port forward...$(RESET)"
	@echo "Stock tool will be available at: http://localhost:8082"
	@echo "Press Ctrl+C to stop"
	@kubectl port-forward -n $(NAMESPACE) svc/stock-service 8082:80

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
		go build -v -o /tmp/stock-tool-test . && \
		echo "$(GREEN)✓ Tool builds successfully (standalone)$(RESET)" && \
		rm /tmp/stock-tool-test

check-cluster: ## Check if cluster is running
	@if kind get clusters | grep -q $(CLUSTER_NAME); then \
		echo "$(GREEN)✓ Cluster $(CLUSTER_NAME) is running$(RESET)"; \
		kubectl cluster-info --context kind-$(CLUSTER_NAME); \
	else \
		echo "$(RED)✗ Cluster $(CLUSTER_NAME) not found$(RESET)"; \
		echo "Run: make setup"; \
		exit 1; \
	fi
