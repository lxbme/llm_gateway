# LLM Gateway


[English](README.md) | [中文](docs/README_zh_cn.md)


A lightweight, OpenAI-compatible API gateway with semantic caching and token-based authentication. Drop-in replacement for the OpenAI API endpoint — just point your existing client to this gateway.

## Features

- **OpenAI API compatible** — works with any client that supports the OpenAI Chat Completions API (`/v1/chat/completions`)
- **Semantic caching** — uses vector similarity search (Qdrant) to serve cached responses for semantically equivalent prompts, significantly reducing upstream API calls and latency
- **Token-based auth** — `sk-xxx` style API keys stored in Redis, with format validation (CRC32 checksum) before any network lookup
- **Streaming support** — full SSE streaming passthrough and cached-response streaming simulation
- **Microservice architecture** — each concern (embedding, cache, completion, auth) is a separate gRPC service, independently deployable and scalable
- **CORS ready** — built-in CORS middleware for browser-based clients

## Architecture

![Architecture-light](./docs/llm_gateway_struct_light.png#gh-light-mode-only)

![Architecture-dark](./docs/llm_gateway_struct_dark.png#gh-dark-mode-only)

## Quick Start

### Prerequisites

- Docker / Podman + Compose
- An OpenAI-compatible API key and endpoint (for embedding and completion)

### 1. Clone and configure

```sh
git clone https://github.com/lxbme/llm_gateway.git
cd llm_gateway
cp .env.example .env
# Edit .env with your API keys and endpoints
```

### 2. Start all services

```sh
podman compose -f docker-compose.prod.yml up -d
# or
docker compose -f docker-compose.prod.yml up -d
```

### 3. Create your first API token

```sh
curl -s -X POST http://localhost:8081/admin/create \
     -H "Content-Type: application/json" \
     -d '{"alias": "my-user"}'
# Response: {"token":"sk_xxx","alias":"my-user"}
```

### 4. Make a request

```sh
curl -s http://localhost:8080/v1/chat/completions \
     -H "Authorization: Bearer sk_xxx" \
     -H "Content-Type: application/json" \
     -d '{
       "model": "gpt-4o-mini",
       "stream": true,
       "messages": [{"role": "user", "content": "Hello!"}]
     }'
```

## Example `.env`

```env
# Embedding service
EMBED_PROVIDER=openai
EMBED_API_KEY=sk-your-embedding-api-key
EMBED_ENDPOINT=https://api.openai.com/v1/embeddings
EMBED_MODEL=text-embedding-3-small
EMBED_DIMENSIONS=1536

# Completion service
COMPL_API_KEY=sk-your-completion-api-key
COMPL_ENDPOINT=https://api.openai.com/v1/chat/completions

# Redis auth DB index
REDIS_DB=0

# Gateway log level: DEBUG | INFO | ERROR
LOG_LEVEL=ERROR

# Enable pprof profiling endpoint
DEBUG_MODE=false

# Admin API secret — REQUIRED to use /admin/* endpoints.
# If unset, all admin requests will be rejected with 403 Forbidden.
# Pass this value via the X-Admin-Secret request header.
ADMIN_SECRET=change-me-to-a-strong-random-secret
```

## Configuration Reference

### Gateway (`gateway`)

| Variable | Default | Description |
|----------|---------|-------------|
| `CACHE_ADDR` | `localhost:50052` | Cache service gRPC address |
| `COMPL_ADDR` | `localhost:50053` | Completion service gRPC address |
| `AUTH_ADDR` | `localhost:50054` | Auth service gRPC address |
| `LOG_LEVEL` | `ERROR` | Log verbosity: `DEBUG`, `INFO`, `ERROR` |
| `DEBUG_MODE` | `false` | Set `true` to enable `/debug/pprof/*` endpoints |

### Embedding Service (`embedding-service`)

| Variable | Default | Description |
|----------|---------|-------------|
| `EMBED_PROVIDER` | — | **Required.** Embedding provider name, currently `openai` |
| `EMBED_API_KEY` | — | API key for the embedding provider |
| `EMBED_ENDPOINT` | — | **Required.** Embedding API endpoint URL |
| `EMBED_MODEL` | — | **Required.** Embedding model name |
| `EMBED_DIMENSIONS` | — | **Required.** Embedding vector dimensions |
| `SERVE_PORT` | `50051` | gRPC listen port |

### Cache Service (`cache-service`)

| Variable | Default | Description |
|----------|---------|-------------|
| `EMBED_ADDR` | `localhost:50051` | Embedding service gRPC address |
| `QDRANT_HOST` | — | Qdrant hostname |
| `QDRANT_PORT` | — | Qdrant gRPC port |
| `SERVE_PORT` | `50052` | gRPC listen port |

### Completion Service (`completion-service`)

| Variable | Default | Description |
|----------|---------|-------------|
| `COMPL_API_KEY` | — | API key for the completion provider |
| `COMPL_ENDPOINT` | — | **Required.** Chat completions API endpoint URL |
| `SERVE_PORT` | `50053` | gRPC listen port |

### Auth Service (`auth-service`)

| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_ADDR` | — | **Required.** Redis address (`host:port`) |
| `REDIS_PASSWORD` | `""` | Redis password (leave empty if none) |
| `REDIS_DB` | — | **Required.** Redis database index |
| `SERVE_PORT` | `50054` | gRPC listen port |

***REDIS_ADDR** is pointing to redis docker in default docker compose file.* 

## Admin API

The admin API listens on `:8081` (bound to `127.0.0.1` only).

**Authentication is required.** Every request must include the `X-Admin-Secret` header matching the `ADMIN_SECRET` environment variable set on the gateway. If `ADMIN_SECRET` is empty or the header is missing/wrong, the request is rejected with `403 Forbidden` — all admin endpoints are effectively disabled until the secret is configured.

```sh
curl -s -X POST http://localhost:8081/admin/create \
     -H "X-Admin-Secret: your-secret" \
     -H "Content-Type: application/json" \
     -d '{"alias": "alice"}'
```

| Method | Path | Body | Description |
|--------|------|------|-------------|
| `POST` | `/admin/create` | `{"alias": "name"}` | Generate a new `sk_xxx` token |
| `POST` | `/admin/get` | `{"token": "sk_xxx"}` | Look up a token's validity and alias |
| `POST` | `/admin/delete` | `{"token": "sk_xxx"}` | Revoke a token |

### Gateway admin configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `ADMIN_SECRET` | — | **Required.** Shared secret for `X-Admin-Secret` header. Admin API is disabled when empty. |

## License

See [LICENSE](LICENSE).

