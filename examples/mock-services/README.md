# Mock Services

This directory contains mock backend services used by TruvaG3 examples for testing and development purposes.

## Available Services

| Service | Port | Description |
|---------|------|-------------|
| `grocery-store-api` | 8081 | Resilience backend expected by the paired setup; source is not tracked in this checkout |
| `product-catalog-api` | 8081 | Incident-response backend expected by the paired setup; source is not tracked in this checkout |
| [enterprise-ollama-gateway](./enterprise-ollama-gateway/) | 18080 | Enterprise OAuth + Azure-style chat API simulator backed by local Ollama |

The first two source directories are unavailable in the tracked repository.
`agent-with-resilience` and `event-driven-agent` setup scripts still reference
them. Their complete mock-backed scenarios require those dependencies to be
supplied and verified separately; a fresh clone alone is insufficient. The
enterprise Ollama simulator is present. This is a checkout prerequisite, not an
instruction to create or deploy a substitute backend automatically.

## Purpose

These mock services provide:

1. **Realistic Backend Simulation**: Services that behave like real APIs
2. **Error Injection**: Configurable failure modes for testing resilience patterns
3. **Self-Contained Examples**: No external dependencies required

## Usage

Each mock service can be:

1. **Run locally** for development
2. **Deployed to Docker** for containerized testing
3. **Deployed to Kubernetes** alongside TruvaG3 agents

See individual service directories for specific instructions.
