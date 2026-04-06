# LLM Gateway


[English](README.md) | [中文](docs/README_zh_cn.md)


A lightweight, OpenAI-compatible API gateway with semantic caching, RAG (Retrieval-Augmented Generation), and token-based authentication. Drop-in replacement for the OpenAI API endpoint — just point your existing client to this gateway.

## Features

- **OpenAI API compatible** — works with any client that supports the OpenAI Chat Completions API (`/v1/chat/completions`)
- **Semantic caching** — uses vector similarity search (Qdrant) to serve cached responses for semantically equivalent prompts, significantly reducing upstream API calls and latency
- **RAG (Retrieval-Augmented Generation)** — optional knowledge-base injection: upload document chunks via the admin API and the gateway automatically retrieves relevant context and prepends it to every prompt
- **Token-based auth** — `sk-xxx` style API keys stored in Redis, with format validation (CRC32 checksum) before any network lookup
- **Streaming support** — full SSE streaming passthrough and cached-response streaming simulation
- **Microservice architecture** — each concern (embedding, cache, completion, auth, rag) is a separate gRPC service, independently deployable and scalable
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
bash -lc 'set -a; source test/cli/.env; set +a; curl -sS -X POST http://127.0.0.1:8081/admin/create -H "X-Admin-Secret: $ADMIN_SECRET" -H "Content-Type: application/json" -d "{\"alias\":\"manual-test\"}"'
# Response: {"token":"sk_xxx","alias":"my-user"}
```

### 4. Make a request
sk_xxx is obtained by running the previous command
```sh
curl -s http://localhost:8080/v1/chat/completions \
     -H "Authorization: Bearer sk_xxx" \
     -H "Content-Type: application/json" \
     -d '{
       "model": "gpt-4o-mini",
       "stream": true,
       "messages": [{"role": "user", "content": "Hello! llm_gateway"}]
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

# Cache service
CACHE_MODE=semantic
CACHE_STORE_PROVIDER=qdrant
CACHE_BUFFER_SIZE=1000
CACHE_WORKER_COUNT=5
QDRANT_SIMILARITY_THRESHOLD=0.95
# QDRANT_COLLECTION_NAME defaults to llm_semantic_cache for the cache service

# Completion service
COMPL_API_KEY=sk-your-completion-api-key
COMPL_ENDPOINT=https://api.openai.com/v1/chat/completions

# RAG service (optional — leave RAG_ADDR empty to disable RAG entirely)
# RAG_ADDR=localhost:50055
# RAG_SIMILARITY_THRESHOLD=0.6   # default 0.6
# RAG_DEFAULT_TOP_K=3            # default 3
# QDRANT_COLLECTION_NAME defaults to llm_rag_documents for the rag service

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
| `RAG_ADDR` | `""` | RAG service gRPC address. Leave empty to disable RAG. |
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
| `CACHE_MODE` | `semantic` | Cache mode. `semantic` requires an embedding service; `exact` is reserved for future exact-match stores |
| `CACHE_STORE_PROVIDER` | — | **Required.** Store provider name, currently `qdrant` |
| `CACHE_PROVIDER` | — | Backward-compatible alias for `CACHE_STORE_PROVIDER` |
| `CACHE_BUFFER_SIZE` | `1000` | Async cache write queue capacity |
| `CACHE_WORKER_COUNT` | `5` | Number of async cache workers |
| `EMBED_ADDR` | `localhost:50051` | Embedding service gRPC address |
| `QDRANT_HOST` | `localhost` | Qdrant hostname |
| `QDRANT_PORT` | `6334` | Qdrant gRPC port |
| `QDRANT_COLLECTION_NAME` | `llm_semantic_cache` | Qdrant collection used by the semantic cache |
| `QDRANT_SIMILARITY_THRESHOLD` | `0.95` | Minimum cosine similarity score required for a cache hit |
| `SERVE_PORT` | `50052` | gRPC listen port |

`cache-service` now selects its backend using `CACHE_MODE + CACHE_STORE_PROVIDER`. In the current implementation only `semantic + qdrant` is available. When `CACHE_MODE=semantic`, `EMBED_ADDR` must point to a reachable embedding service so the cache can generate vectors before searching or writing.

### Completion Service (`completion-service`)

| Variable | Default | Description |
|----------|---------|-------------|
| `COMPL_API_KEY` | — | API key for the completion provider |
| `COMPL_ENDPOINT` | — | **Required.** Chat completions API endpoint URL |
| `SERVE_PORT` | `50053` | gRPC listen port |

### RAG Service (`rag-service`)

| Variable | Default | Description |
|----------|---------|-------------|
| `EMBED_ADDR` | `localhost:50051` | Embedding service gRPC address |
| `QDRANT_HOST` | `localhost` | Qdrant hostname |
| `QDRANT_PORT` | `6334` | Qdrant gRPC port |
| `QDRANT_COLLECTION_NAME` | `llm_rag_documents` | Qdrant collection for RAG chunks (separate from the cache collection) |
| `RAG_SIMILARITY_THRESHOLD` | `0.6` | Minimum cosine similarity for a chunk to be retrieved |
| `RAG_DEFAULT_TOP_K` | `3` | Maximum number of chunks returned per query |
| `SERVE_PORT` | `50055` | gRPC listen port |

### Auth Service (`auth-service`)

| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_ADDR` | — | **Required.** Redis address (`host:port`) |
| `REDIS_PASSWORD` | `""` | Redis password (leave empty if none) |
| `REDIS_DB` | — | **Required.** Redis database index |
| `SERVE_PORT` | `50054` | gRPC listen port |

***REDIS_ADDR** is pointing to redis docker in default docker compose file.* 

## RAG (Retrieval-Augmented Generation)

The gateway ships an optional `rag-service` that lets you upload a knowledge base and have relevant document chunks automatically injected into every LLM prompt.

### How it works

```
User request
  └─ [Gateway: StageBeforeUpstream]
       ├─ auth_validate_handler   ← verify token, resolve alias
       ├─ rag_retrieve_handler    ← query rag-service for top-K chunks
       │     └─ augment PromptText with retrieved context
       ├─ cache_lookup_handler    ← operate on the augmented prompt
       └─ upstream_request_build_handler
```

The RAG handler is a **soft dependency**: if `RAG_ADDR` is not set, or if retrieval returns no results, the request proceeds normally without any modification.

### Knowledge-base isolation

Document chunks are stored in a single Qdrant collection (`llm_rag_documents`) with a `collection` payload field used as a filter. Each request selects its knowledge base via:

1. **`X-RAG-Collection` request header** — explicit collection name (takes priority)
2. **Token alias** — falls back to the alias of the authenticated token (per-user knowledge base)

### Starting the RAG service

```sh
# environment variables
EMBED_ADDR=localhost:50051
QDRANT_HOST=localhost
QDRANT_PORT=6334
QDRANT_COLLECTION_NAME=llm_rag_documents
RAG_SIMILARITY_THRESHOLD=0.6
RAG_DEFAULT_TOP_K=3
SERVE_PORT=50055

go run ./cmd/rag
```

Then tell the gateway where to find it:

```sh
RAG_ADDR=localhost:50055 go run ./cmd/gateway
```

### Uploading documents

Split your document into text chunks and POST them to the admin API:

```sh
curl -X POST http://localhost:8081/admin/rag/ingest \
     -H "X-Admin-Secret: your-secret" \
     -H "Content-Type: application/json" \
     -d '{
       "collection": "alice",
       "source":     "docs/product-faq.md",
       "chunks": [
         {"content": "Our refund policy is 30 days...", "chunk_index": 0, "total_chunks": 3},
         {"content": "To request a refund, email...",   "chunk_index": 1, "total_chunks": 3},
         {"content": "Refunds are processed within...", "chunk_index": 2, "total_chunks": 3}
       ]
     }'
# Response: {"doc_id":"<uuid>","ingested_count":3}
```

### Making a RAG-enabled request

```sh
curl -s http://localhost:8080/v1/chat/completions \
     -H "Authorization: Bearer sk_xxx" \
     -H "X-RAG-Collection: alice" \
     -H "Content-Type: application/json" \
     -d '{
       "model": "gpt-4o-mini",
       "stream": true,
       "messages": [{"role": "user", "content": "What is your refund policy?"}]
     }'
```

The gateway retrieves the top-3 matching chunks from the `alice` collection and prepends them to the prompt before calling the LLM.

### Deleting a document

```sh
curl -X DELETE http://localhost:8081/admin/rag/doc \
     -H "X-Admin-Secret: your-secret" \
     -H "Content-Type: application/json" \
     -d '{"doc_id": "<uuid>", "collection": "alice"}'
# Response: 204 No Content
```

---

## Admin API

The admin API listens on `:8081` (bound to `127.0.0.1` only).

**Authentication is required.** Every request must include the `X-Admin-Secret` header matching the `ADMIN_SECRET` environment variable set on the gateway. If `ADMIN_SECRET` is empty or the header is missing/wrong, the request is rejected with `403 Forbidden` — all admin endpoints are effectively disabled until the secret is configured.

```sh
curl -s -X POST http://localhost:8081/admin/create \
     -H "X-Admin-Secret: your-secret" \
     -H "Content-Type: application/json" \
     -d '{"alias": "alice"}'
```

**Token management**

| Method | Path | Body | Description |
|--------|------|------|-------------|
| `POST` | `/admin/create` | `{"alias": "name"}` | Generate a new `sk_xxx` token |
| `POST` | `/admin/get` | `{"token": "sk_xxx"}` | Look up a token's validity and alias |
| `POST` | `/admin/delete` | `{"token": "sk_xxx"}` | Revoke a token |

**RAG knowledge-base management**

| Method | Path | Body | Description |
|--------|------|------|-------------|
| `POST` | `/admin/rag/ingest` | `{"collection","source","chunks":[...]}` | Ingest document chunks into a collection |
| `DELETE` | `/admin/rag/doc` | `{"doc_id","collection"}` | Delete all chunks of a document |

### Gateway admin configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `ADMIN_SECRET` | — | **Required.** Shared secret for `X-Admin-Secret` header. Admin API is disabled when empty. |

## License

See [LICENSE](LICENSE).

