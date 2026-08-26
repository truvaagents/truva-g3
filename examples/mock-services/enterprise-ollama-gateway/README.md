# Enterprise Ollama Gateway Simulator

This local test server reproduces an enterprise OAuth2 and Azure-style chat API
contract while using a locally running Ollama model for inference.

It exposes:

- `POST /oauth2/token` — OAuth2 client-credentials with HTTP Basic auth.
- `POST /openai/deployments/{deployment}/chat/completions` — requires the issued
  token in `api-key`, a JSON-string `user.appkey`, the captured stop sequence,
  and `stream: false`.
- `GET /health` — verifies that Ollama is reachable.

The chat endpoint forwards `messages`, `temperature`, `max_tokens`, and `stop`
to Ollama's native `/api/chat` endpoint. It maps the Ollama result back into a
standard OpenAI `chat.completion` response and adds the Azure-style filter,
usage-detail, and latency fields from the captured response.

## Run it

Prerequisites: Go 1.27+, Ollama, and `curl`.

```bash
cd examples/mock-services/enterprise-ollama-gateway
cp .env.example .env
ollama pull llama3.2
./setup.sh run
```

In another terminal, exercise the exact two-step exchange:

```bash
./setup.sh smoke
```

The smoke command obtains a short-lived token and sends a representative chat
request using the configured authentication and deployment values. The response
content comes from Ollama; the surrounding JSON is the simulated enterprise/OpenAI
response.

## Connect the travel-chat-agent locally

Load the same values before starting the agent:

```bash
set -a
source examples/mock-services/enterprise-ollama-gateway/.env
set +a
cd examples/travel-chat-agent
./setup.sh run-all
```

The relevant client-side values are:

```text
ENTERPRISE_BASE_HOST=http://127.0.0.1:18080
ENTERPRISE_TOKEN_URL=http://127.0.0.1:18080/oauth2/token
ENTERPRISE_CLIENT_ID=travel-chat-agent
ENTERPRISE_CLIENT_SECRET=local-enterprise-secret
ENTERPRISE_APP_KEY=travel-chat-app
ENTERPRISE_DEPLOYMENT=gpt-4o-mini
```

This is intentionally a local simulator. If the travel agent runs inside Kind,
`127.0.0.1` points to the pod rather than the host; use a host-reachable address
for the simulator and ensure the `ENTERPRISE_*` variables are propagated into
the pod.

## Contract tests

```bash
./setup.sh test
```

The tests use an in-process fake Ollama server, so they do not require Ollama.
They cover token issuance/expiry, custom-header enforcement, app-key and stop
validation, request mapping, OpenAI response mapping, and upstream failures.
