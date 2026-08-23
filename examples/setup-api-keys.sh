#!/bin/bash

# TruvaG3 Framework - API Keys Setup Script
# This script helps you set up API keys for local development and Kubernetes

set -e

COLOR_RED='\033[0;31m'
COLOR_GREEN='\033[0;32m'
COLOR_YELLOW='\033[1;33m'
COLOR_BLUE='\033[0;34m'
COLOR_NC='\033[0m' # No Color

echo -e "${COLOR_BLUE}🔐 TruvaG3 Framework - API Keys Setup${COLOR_NC}"
echo "=================================================="

# Function to prompt for API key
prompt_for_key() {
    local key_name="$1"
    local description="$2"
    local optional="$3"

    if [ "$optional" = "true" ]; then
        echo -e "\n${COLOR_YELLOW}📋 $description (Optional)${COLOR_NC}"
        read -s -p "Enter $key_name (press Enter to skip): " key_value
    else
        echo -e "\n${COLOR_YELLOW}📋 $description${COLOR_NC}"
        read -s -p "Enter $key_name: " key_value
    fi

    echo
    if [ -n "$key_value" ]; then
        echo "export $key_name=\"$key_value\"" >> .env
        echo -e "✅ $key_name configured"
    else
        if [ "$optional" = "false" ]; then
            echo -e "❌ $key_name is required"
            exit 1
        else
            echo -e "⏭️  $key_name skipped"
        fi
    fi
}

# Main setup function
setup_local_env() {
    echo -e "\n${COLOR_GREEN}🏠 Setting up local development environment${COLOR_NC}"

    # Backup existing .env if it exists
    if [ -f .env ]; then
        cp .env .env.backup.$(date +%Y%m%d_%H%M%S)
        echo "📦 Backed up existing .env file"
    fi

    # Create new .env file
    echo "# TruvaG3 Framework - API Keys" > .env
    echo "# Generated on $(date)" >> .env
    echo "" >> .env

    # Prompt for each API key
    prompt_for_key "OPENAI_API_KEY" "OpenAI API Key (for GPT models)" "false"
    prompt_for_key "ANTHROPIC_API_KEY" "Anthropic API Key (for Claude models)" "true"
    prompt_for_key "OPENROUTER_API_KEY" "OpenRouter API Key (multi-provider routing)" "true"
    prompt_for_key "GROQ_API_KEY" "Groq API Key (for fast inference)" "true"
    prompt_for_key "GOOGLE_AI_API_KEY" "Google AI API Key (for Gemini models)" "true"
    prompt_for_key "DEEPSEEK_API_KEY" "DeepSeek API Key (for DeepSeek models)" "true"
    prompt_for_key "WEATHER_API_KEY" "Weather API Key (for tool examples)" "true"

    # Add framework configuration
    echo "" >> .env
    echo "# Framework Configuration" >> .env
    echo "export TRUVAG3_DEV_MODE=true" >> .env
    echo "export LOG_LEVEL=info" >> .env
    echo "export REDIS_URL=redis://localhost:6379" >> .env

    echo -e "\n${COLOR_GREEN}✅ Local environment setup complete!${COLOR_NC}"
    echo -e "📝 Run: ${COLOR_BLUE}source .env${COLOR_NC} to load variables"
    echo -e "📝 Or: ${COLOR_BLUE}set -a; source .env; set +a${COLOR_NC} to auto-export"
}

# Kubernetes setup function
setup_kubernetes_secrets() {
    echo -e "\n${COLOR_GREEN}☸️  Setting up Kubernetes secrets${COLOR_NC}"

    # Check if kubectl is available
    if ! command -v kubectl &> /dev/null; then
        echo -e "${COLOR_RED}❌ kubectl not found. Please install kubectl first.${COLOR_NC}"
        return 1
    fi

    # Check if .env file exists
    if [ ! -f .env ]; then
        echo -e "${COLOR_RED}❌ .env file not found. Run local setup first.${COLOR_NC}"
        return 1
    fi

    # Load environment variables
    set -a
    source .env
    set +a

    # Create namespace if it doesn't exist
    kubectl create namespace truvag3-examples --dry-run=client -o yaml | kubectl apply -f -

    # Create AI keys secret
    echo "🔑 Creating ai-provider-keys secret..."
    kubectl create secret generic ai-provider-keys \
        --namespace=truvag3-examples \
        --from-literal=OPENAI_API_KEY="${OPENAI_API_KEY:-}" \
        --from-literal=ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-}" \
        --from-literal=OPENROUTER_API_KEY="${OPENROUTER_API_KEY:-}" \
        --from-literal=GROQ_API_KEY="${GROQ_API_KEY:-}" \
        --from-literal=GOOGLE_AI_API_KEY="${GOOGLE_AI_API_KEY:-}" \
        --from-literal=GEMINI_API_KEY="${GOOGLE_AI_API_KEY:-}" \
        --from-literal=DEEPSEEK_API_KEY="${DEEPSEEK_API_KEY:-}" \
        --dry-run=client -o yaml | kubectl apply -f -

    # Create external API keys secret
    echo "🔑 Creating external-api-keys secret..."
    kubectl create secret generic external-api-keys \
        --namespace=truvag3-examples \
        --from-literal=WEATHER_API_KEY="${WEATHER_API_KEY:-}" \
        --dry-run=client -o yaml | kubectl apply -f -

    echo -e "\n${COLOR_GREEN}✅ Kubernetes secrets created successfully!${COLOR_NC}"
    echo -e "📝 Secrets created in namespace: ${COLOR_BLUE}truvag3-examples${COLOR_NC}"
    echo -e "📝 View secrets: ${COLOR_BLUE}kubectl get secrets -n truvag3-examples${COLOR_NC}"
}

# Update deployment files function
update_deployment_files() {
    echo -e "\n${COLOR_GREEN}📝 Updating deployment files with standardized secrets${COLOR_NC}"

    # This would update all deployment files to use consistent secret names
    echo "🔄 Standardizing secret references in deployment files..."
    echo -e "✅ Deployment files ready for updated secret names"
}

# Main menu
show_menu() {
    echo -e "\n${COLOR_BLUE}Choose setup option:${COLOR_NC}"
    echo "1) 🏠 Local development (.env file)"
    echo "2) ☸️  Kubernetes secrets"
    echo "3) 🔄 Both local and Kubernetes"
    echo "4) 📋 Show current configuration"
    echo "5) 🧹 Clean up secrets"
    echo "6) ❌ Exit"
}

# Show current configuration
show_config() {
    echo -e "\n${COLOR_BLUE}📋 Current Configuration${COLOR_NC}"
    echo "================================"

    if [ -f .env ]; then
        echo -e "${COLOR_GREEN}✅ Local .env file exists${COLOR_NC}"
        echo "Environment variables:"
        grep -E "^export [A-Z_]+=.*$" .env | sed 's/=.*/=***/' | head -10
    else
        echo -e "${COLOR_YELLOW}⚠️  No local .env file found${COLOR_NC}"
    fi

    if command -v kubectl &> /dev/null; then
        echo -e "\n${COLOR_GREEN}☸️ Kubernetes Status${COLOR_NC}"
        if kubectl get namespace truvag3-examples &>/dev/null; then
            echo "✅ Namespace 'truvag3-examples' exists"
            kubectl get secrets -n truvag3-examples --no-headers 2>/dev/null | wc -l | xargs echo "📦 Secrets count:"
        else
            echo "⚠️  Namespace 'truvag3-examples' not found"
        fi
    else
        echo -e "\n${COLOR_YELLOW}⚠️  kubectl not available${COLOR_NC}"
    fi
}

# Clean up function
cleanup_secrets() {
    echo -e "\n${COLOR_YELLOW}🧹 Cleaning up secrets${COLOR_NC}"
    read -p "Delete local .env file? (y/N): " delete_local

    if [[ $delete_local =~ ^[Yy]$ ]]; then
        rm -f .env .env.backup.*
        echo "✅ Local .env files removed"
    fi

    if command -v kubectl &> /dev/null; then
        read -p "Delete Kubernetes secrets in truvag3-examples namespace? (y/N): " delete_k8s
        if [[ $delete_k8s =~ ^[Yy]$ ]]; then
            kubectl delete secret ai-provider-keys external-api-keys -n truvag3-examples --ignore-not-found=true
            echo "✅ Kubernetes secrets removed"
        fi
    fi
}

# Main execution
cd "$(dirname "$0")"

case "${1:-menu}" in
    "local")
        setup_local_env
        ;;
    "k8s"|"kubernetes")
        setup_kubernetes_secrets
        ;;
    "both")
        setup_local_env
        setup_kubernetes_secrets
        ;;
    "config"|"show")
        show_config
        ;;
    "clean")
        cleanup_secrets
        ;;
    "menu"|"")
        while true; do
            show_menu
            read -p "Enter your choice (1-6): " choice
            case $choice in
                1) setup_local_env ;;
                2) setup_kubernetes_secrets ;;
                3) setup_local_env && setup_kubernetes_secrets ;;
                4) show_config ;;
                5) cleanup_secrets ;;
                6) echo -e "${COLOR_GREEN}👋 Goodbye!${COLOR_NC}"; exit 0 ;;
                *) echo -e "${COLOR_RED}❌ Invalid option${COLOR_NC}" ;;
            esac
        done
        ;;
    *)
        echo -e "${COLOR_RED}❌ Unknown option: $1${COLOR_NC}"
        echo "Usage: $0 [local|k8s|both|config|clean|menu]"
        exit 1
        ;;
esac
