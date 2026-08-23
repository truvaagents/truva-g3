#!/bin/bash

# TruvaG3 Framework - Kind Demo Setup Script
# This script creates a complete Kind-based demo environment

set -e

COLOR_RED='\033[0;31m'
COLOR_GREEN='\033[0;32m'
COLOR_YELLOW='\033[1;33m'
COLOR_BLUE='\033[0;34m'
COLOR_PURPLE='\033[0;35m'
COLOR_NC='\033[0m' # No Color

CLUSTER_NAME=${CLUSTER_NAME:-truvag3-demo}
NAMESPACE=${NAMESPACE:-truvag3-examples}

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLES_DIR="$SCRIPT_DIR"
K8S_DIR="$SCRIPT_DIR/k8-deployment"

echo -e "${COLOR_BLUE}"
echo "🚀 TruvaG3 Framework - Kind Demo Setup"
echo "======================================"
echo -e "${COLOR_NC}"

# Function to check dependencies
check_dependencies() {
    echo -e "${COLOR_YELLOW}🔍 Checking dependencies...${COLOR_NC}"

    local missing_deps=()

    if ! command -v kind &> /dev/null; then
        missing_deps+=("kind")
    fi

    if ! command -v kubectl &> /dev/null; then
        missing_deps+=("kubectl")
    fi

    if ! command -v docker &> /dev/null; then
        missing_deps+=("docker")
    fi

    if [ ${#missing_deps[@]} -ne 0 ]; then
        echo -e "${COLOR_RED}❌ Missing dependencies: ${missing_deps[*]}${COLOR_NC}"
        echo -e "${COLOR_YELLOW}Please install the missing dependencies and try again.${COLOR_NC}"
        echo ""
        echo "Installation instructions:"
        echo "- kind: https://kind.sigs.k8s.io/docs/user/quick-start/#installation"
        echo "- kubectl: https://kubernetes.io/docs/tasks/tools/"
        echo "- docker: https://docs.docker.com/get-docker/"
        exit 1
    fi

    echo -e "${COLOR_GREEN}✅ All dependencies found${COLOR_NC}"
}

# Function to create Kind cluster
create_cluster() {
    echo -e "\n${COLOR_YELLOW}🏗️ Creating Kind cluster: ${CLUSTER_NAME}${COLOR_NC}"

    if kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
        echo -e "${COLOR_YELLOW}⚠️ Cluster '${CLUSTER_NAME}' already exists${COLOR_NC}"
        read -p "Do you want to delete and recreate it? (y/N): " -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            kind delete cluster --name "${CLUSTER_NAME}"
        else
            echo -e "${COLOR_BLUE}📋 Using existing cluster${COLOR_NC}"
            kubectl cluster-info --context "kind-${CLUSTER_NAME}"
            return 0
        fi
    fi

    # Create cluster with port forwarding for services
    cat << EOF | kind create cluster --name "${CLUSTER_NAME}" --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: 30080
    hostPort: 8080
    protocol: TCP
  - containerPort: 30090
    hostPort: 8090
    protocol: TCP
  - containerPort: 30000
    hostPort: 3000
    protocol: TCP
  - containerPort: 31686
    hostPort: 16686
    protocol: TCP
  - containerPort: 39090
    hostPort: 9090
    protocol: TCP
EOF

    echo -e "${COLOR_GREEN}✅ Kind cluster created successfully${COLOR_NC}"

    # Set kubectl context
    kubectl config use-context "kind-${CLUSTER_NAME}"

    echo -e "${COLOR_BLUE}📋 Cluster info:${COLOR_NC}"
    kubectl cluster-info
}

# Function to setup namespace and secrets
setup_namespace() {
    echo -e "\n${COLOR_YELLOW}📁 Setting up namespace and secrets...${COLOR_NC}"

    # Create namespace
    kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -

    # Setup API keys if available
    if [ -f "$SCRIPT_DIR/.env" ]; then
        echo -e "${COLOR_BLUE}🔑 Found .env file, setting up secrets...${COLOR_NC}"
        source "$SCRIPT_DIR/.env"

        kubectl create secret generic ai-provider-keys \
            --namespace="${NAMESPACE}" \
            --from-literal=OPENAI_API_KEY="${OPENAI_API_KEY:-}" \
            --from-literal=ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-}" \
            --from-literal=OPENROUTER_API_KEY="${OPENROUTER_API_KEY:-}" \
            --from-literal=GROQ_API_KEY="${GROQ_API_KEY:-}" \
            --from-literal=GOOGLE_AI_API_KEY="${GOOGLE_AI_API_KEY:-}" \
            --from-literal=GEMINI_API_KEY="${GOOGLE_AI_API_KEY:-}" \
            --from-literal=DEEPSEEK_API_KEY="${DEEPSEEK_API_KEY:-}" \
            --dry-run=client -o yaml | kubectl apply -f -

        kubectl create secret generic external-api-keys \
            --namespace="${NAMESPACE}" \
            --from-literal=WEATHER_API_KEY="${WEATHER_API_KEY:-demo-key}" \
            --dry-run=client -o yaml | kubectl apply -f -

        echo -e "${COLOR_GREEN}✅ API keys configured from .env file${COLOR_NC}"
    else
        echo -e "${COLOR_YELLOW}⚠️ No .env file found. Creating secrets with empty values.${COLOR_NC}"
        echo -e "${COLOR_BLUE}💡 Run ./setup-api-keys.sh first to configure API keys${COLOR_NC}"

        kubectl create secret generic ai-provider-keys \
            --namespace="${NAMESPACE}" \
            --from-literal=OPENAI_API_KEY="" \
            --from-literal=ANTHROPIC_API_KEY="" \
            --from-literal=OPENROUTER_API_KEY="" \
            --from-literal=GROQ_API_KEY="" \
            --dry-run=client -o yaml | kubectl apply -f -

        kubectl create secret generic external-api-keys \
            --namespace="${NAMESPACE}" \
            --from-literal=WEATHER_API_KEY="demo-key" \
            --dry-run=client -o yaml | kubectl apply -f -
    fi
}

# Function to deploy infrastructure
deploy_infrastructure() {
    echo -e "\n${COLOR_YELLOW}🏗️ Deploying monitoring infrastructure...${COLOR_NC}"

    if [ ! -d "$K8S_DIR" ]; then
        echo -e "${COLOR_RED}❌ k8-deployment directory not found at ${K8S_DIR}${COLOR_NC}"
        exit 1
    fi

    # Deploy in order (dependencies first)
    local components=("namespace.yaml" "redis.yaml" "otel-collector.yaml" "jaeger.yaml" "prometheus.yaml" "grafana.yaml")

    for component in "${components[@]}"; do
        if [ -f "$K8S_DIR/$component" ]; then
            echo -e "${COLOR_BLUE}📦 Deploying ${component}...${COLOR_NC}"
            kubectl apply -f "$K8S_DIR/$component"
        else
            echo -e "${COLOR_YELLOW}⚠️ ${component} not found, skipping...${COLOR_NC}"
        fi
    done

    echo -e "${COLOR_GREEN}✅ Infrastructure deployed${COLOR_NC}"
}

# Function to build and deploy examples
deploy_examples() {
    echo -e "\n${COLOR_YELLOW}🎯 Building and deploying TruvaG3 examples...${COLOR_NC}"

    # Build Docker images for examples
    local examples=("tool-example" "agent-example" "ai-agent-example")

    for example in "${examples[@]}"; do
        if [ -d "$EXAMPLES_DIR/$example" ]; then
            echo -e "${COLOR_BLUE}🔨 Building ${example}...${COLOR_NC}"

            # Build Docker image
            (cd "$EXAMPLES_DIR/$example" && docker build -t "${example}:latest" .)

            # Load image into Kind cluster
            kind load docker-image "${example}:latest" --name "${CLUSTER_NAME}"

            # Deploy to Kubernetes
            if [ -f "$EXAMPLES_DIR/$example/k8-deployment.yaml" ]; then
                echo -e "${COLOR_BLUE}📦 Deploying ${example}...${COLOR_NC}"
                kubectl apply -f "$EXAMPLES_DIR/$example/k8-deployment.yaml"
            else
                echo -e "${COLOR_YELLOW}⚠️ No deployment file for ${example}, skipping deployment...${COLOR_NC}"
            fi
        else
            echo -e "${COLOR_YELLOW}⚠️ Example ${example} not found, skipping...${COLOR_NC}"
        fi
    done

    echo -e "${COLOR_GREEN}✅ Examples deployed${COLOR_NC}"
}

# Function to wait for deployments
wait_for_deployments() {
    echo -e "\n${COLOR_YELLOW}⏳ Waiting for deployments to be ready...${COLOR_NC}"

    # Wait for infrastructure
    local infra_deployments=("redis" "otel-collector" "jaeger" "prometheus" "grafana")

    for deployment in "${infra_deployments[@]}"; do
        echo -e "${COLOR_BLUE}⏳ Waiting for ${deployment}...${COLOR_NC}"
        kubectl wait --for=condition=available --timeout=300s deployment/"${deployment}" -n "${NAMESPACE}" || true
    done

    # Wait for examples (if they exist)
    echo -e "${COLOR_BLUE}⏳ Waiting for TruvaG3 examples...${COLOR_NC}"
    kubectl wait --for=condition=available --timeout=300s deployment -l "truvag3.framework/type" -n "${NAMESPACE}" || true

    echo -e "${COLOR_GREEN}✅ All deployments ready${COLOR_NC}"
}

# Function to setup port forwarding
setup_port_forwarding() {
    echo -e "\n${COLOR_YELLOW}🌐 Setting up port forwarding...${COLOR_NC}"

    # Kill existing port forwards
    pkill -f "kubectl.*port-forward" || true
    sleep 2

    # Start port forwarding in background
    echo -e "${COLOR_BLUE}🔗 Starting port forwards...${COLOR_NC}"

    # Grafana
    kubectl port-forward -n "${NAMESPACE}" svc/grafana 3000:80 &
    GRAFANA_PID=$!

    # Prometheus
    kubectl port-forward -n "${NAMESPACE}" svc/prometheus 9090:9090 &
    PROMETHEUS_PID=$!

    # Jaeger
    kubectl port-forward -n "${NAMESPACE}" svc/jaeger-query 16686:80 &
    JAEGER_PID=$!

    # Tool example (if exists)
    kubectl port-forward -n "${NAMESPACE}" svc/weather-tool-service 8080:80 &
    TOOL_PID=$!

    # Agent example (if exists)
    kubectl port-forward -n "${NAMESPACE}" svc/research-agent-service 8090:80 &
    AGENT_PID=$!

    sleep 3

    echo -e "${COLOR_GREEN}✅ Port forwarding started${COLOR_NC}"

    # Save PIDs for cleanup
    echo "$GRAFANA_PID $PROMETHEUS_PID $JAEGER_PID $TOOL_PID $AGENT_PID" > /tmp/truvag3-port-forwards.pid
}

# Function to show status and URLs
show_status() {
    echo -e "\n${COLOR_GREEN}🎉 TruvaG3 Kind Demo Environment Ready!${COLOR_NC}"
    echo -e "${COLOR_PURPLE}=======================================${COLOR_NC}"
    echo ""
    echo -e "${COLOR_BLUE}📊 Monitoring & Observability:${COLOR_NC}"
    echo -e "   Grafana:    ${COLOR_YELLOW}http://localhost:3000${COLOR_NC} (admin/admin)"
    echo -e "   Prometheus: ${COLOR_YELLOW}http://localhost:9090${COLOR_NC}"
    echo -e "   Jaeger:     ${COLOR_YELLOW}http://localhost:16686${COLOR_NC}"
    echo ""
    echo -e "${COLOR_BLUE}🚀 TruvaG3 Examples:${COLOR_NC}"
    echo -e "   Weather Tool: ${COLOR_YELLOW}http://localhost:8080/health${COLOR_NC}"
    echo -e "   Research Agent: ${COLOR_YELLOW}http://localhost:8090/health${COLOR_NC}"
    echo ""
    echo -e "${COLOR_BLUE}🔧 Kubernetes Commands:${COLOR_NC}"
    echo -e "   Cluster: ${COLOR_YELLOW}kubectl config use-context kind-${CLUSTER_NAME}${COLOR_NC}"
    echo -e "   Namespace: ${COLOR_YELLOW}kubectl get pods -n ${NAMESPACE}${COLOR_NC}"
    echo -e "   Logs: ${COLOR_YELLOW}kubectl logs -n ${NAMESPACE} -l app=<component>${COLOR_NC}"
    echo ""
    echo -e "${COLOR_BLUE}🧹 Cleanup:${COLOR_NC}"
    echo -e "   Stop port forwards: ${COLOR_YELLOW}./setup-kind-demo.sh cleanup${COLOR_NC}"
    echo -e "   Delete cluster: ${COLOR_YELLOW}kind delete cluster --name ${CLUSTER_NAME}${COLOR_NC}"
    echo ""

    # Show pod status
    echo -e "${COLOR_BLUE}📋 Current Pod Status:${COLOR_NC}"
    kubectl get pods -n "${NAMESPACE}" -o wide
    echo ""

    # Show services
    echo -e "${COLOR_BLUE}🌐 Services:${COLOR_NC}"
    kubectl get svc -n "${NAMESPACE}"
}

# Function to cleanup
cleanup() {
    echo -e "${COLOR_YELLOW}🧹 Cleaning up port forwards...${COLOR_NC}"

    if [ -f /tmp/truvag3-port-forwards.pid ]; then
        while read -r pid; do
            kill "$pid" 2>/dev/null || true
        done < /tmp/truvag3-port-forwards.pid
        rm -f /tmp/truvag3-port-forwards.pid
    fi

    # Kill any remaining kubectl port-forward processes
    pkill -f "kubectl.*port-forward" || true

    echo -e "${COLOR_GREEN}✅ Port forwards stopped${COLOR_NC}"
    echo -e "${COLOR_BLUE}💡 To delete the cluster: kind delete cluster --name ${CLUSTER_NAME}${COLOR_NC}"
}

# Function to show help
show_help() {
    echo "TruvaG3 Kind Demo Setup Script"
    echo ""
    echo "Usage: $0 [COMMAND]"
    echo ""
    echo "Commands:"
    echo "  setup       Create Kind cluster and deploy TruvaG3 demo (default)"
    echo "  cleanup     Stop port forwards and cleanup"
    echo "  status      Show current status and URLs"
    echo "  delete      Delete the Kind cluster completely"
    echo "  help        Show this help message"
    echo ""
    echo "Environment Variables:"
    echo "  CLUSTER_NAME    Name of the Kind cluster (default: truvag3-demo)"
    echo "  NAMESPACE       Kubernetes namespace (default: truvag3-examples)"
}

# Function to delete cluster
delete_cluster() {
    echo -e "${COLOR_YELLOW}🗑️ Deleting Kind cluster: ${CLUSTER_NAME}${COLOR_NC}"
    cleanup
    kind delete cluster --name "${CLUSTER_NAME}"
    echo -e "${COLOR_GREEN}✅ Cluster deleted${COLOR_NC}"
}

# Main execution
case "${1:-setup}" in
    "setup")
        check_dependencies
        create_cluster
        setup_namespace
        deploy_infrastructure
        deploy_examples
        wait_for_deployments
        setup_port_forwarding
        show_status
        ;;
    "cleanup")
        cleanup
        ;;
    "status")
        show_status
        ;;
    "delete")
        delete_cluster
        ;;
    "help"|"-h"|"--help")
        show_help
        ;;
    *)
        echo -e "${COLOR_RED}❌ Unknown command: $1${COLOR_NC}"
        echo ""
        show_help
        exit 1
        ;;
esac
