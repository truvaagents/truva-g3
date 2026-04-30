# Mock Services

This directory contains mock backend services used by TruvaG3 examples for testing and development purposes.

## Available Services

| Service | Port | Description |
|---------|------|-------------|
| [grocery-store-api](./grocery-store-api/) | 8081 | Mock grocery store with error injection for resilience testing |
| [product-catalog-api](./product-catalog-api/) | 8081 | Product catalog microservice with Prometheus metrics for E2E incident response testing |

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
