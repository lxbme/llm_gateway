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
cp config/.env.example .env
# Edit .env with your API keys and endpoints. See config/ for more examples
# (multi-endpoint pool, horizontal scaling) and the Configuration Reference
# below for the meaning of every variable.
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

## Example configs

All example configs live in [`config/`](config/) — copy them out and adapt:

| File | Purpose |
|---|---|
| [`config/.env.example`](config/.env.example) | Project root `.env` template; covers every env-driven variable |
| [`config/pool-minimal.example.json`](config/pool-minimal.example.json) | Single-endpoint `completion-service` pool, the smallest JSON-format upgrade from legacy `COMPL_ENDPOINT` |
| [`config/pool.example.json`](config/pool.example.json) | Full-featured pool: multi-endpoint, weighted, circuit breaker, model affinity |
| [`config/docker-compose.scale.example.yml`](config/docker-compose.scale.example.yml) | Horizontal-scaling overlay (`docker compose -f … -f …`) with the pool config mounted into completion-service |

`cp config/.env.example .env` to start; refer to the table below for what every key does.

## Configuration Reference

The gateway is fully env-driven, with one exception: `completion-service` supports a richer JSON config for multi-endpoint upstream pools. See [§ Completion pool JSON](#completion-pool-json-multi-endpoint-mode) below.

### Gateway (`gateway`)

| Variable | Default | Description |
|----------|---------|-------------|
| `CACHE_ADDR` | `localhost:50052` | Cache service gRPC address (fallback when etcd discovery is disabled) |
| `COMPL_ADDR` | `localhost:50053` | Completion service gRPC address (fallback when etcd discovery is disabled) |
| `AUTH_ADDR` | `localhost:50054` | Auth service gRPC address (fallback when etcd discovery is disabled) |
| `RAG_ADDR` | `""` | RAG service gRPC address. Leave empty to disable RAG. |
| `LOG_LEVEL` | `ERROR` | Log verbosity: `DEBUG`, `INFO`, `ERROR` |
| `DEBUG_MODE` | `false` | Set `true` to enable `/debug/pprof/*` endpoints |
| `ADMIN_SECRET` | — | **Required to use `/admin/*`.** Compared against the `X-Admin-Secret` header. Unset → all admin calls 403. |

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

`cache-service` selects its backend using `CACHE_MODE + CACHE_STORE_PROVIDER`. Currently only `semantic + qdrant` is implemented. When `CACHE_MODE=semantic`, `EMBED_ADDR` must point to a reachable embedding service so the cache can generate vectors before searching or writing.

### Completion Service (`completion-service`)

> 📘 **Detailed reference:** [`docs/pool_config.md`](docs/pool_config.md) (or [中文版](docs/pool_config_zh_cn.md)) covers every field, validation rule, strategy, breaker tuning, runtime mutation, multi-replica semantics, and a migration walkthrough. The summary below is for orientation only.

The completion service reads its upstream pool config from one of three sources, in priority order:

1. `COMPL_POOL_CONFIG_FILE` — path to a JSON file (highest)
2. `COMPL_POOL_CONFIG` — inline JSON string
3. `COMPL_ENDPOINT` + `COMPL_API_KEY` — legacy single-endpoint fallback (lowest)

If none are set, the service exits with an error. The single-endpoint legacy path is preserved for backwards compatibility but logs a deprecation warning at startup.

| Variable | Default | Description |
|----------|---------|-------------|
| `COMPL_POOL_CONFIG_FILE` | — | Path to JSON pool config. Highest priority. |
| `COMPL_POOL_CONFIG` | — | Inline JSON pool config. Used only if `COMPL_POOL_CONFIG_FILE` is empty. |
| `COMPL_ENDPOINT` | — | Legacy single-endpoint URL. Used only if neither JSON variable is set. Synthesizes a 1-endpoint pool internally. |
| `COMPL_API_KEY` | — | When in legacy mode, the env var name the openai client reads to authenticate. (i.e., the actual key value is in this env var.) |
| *referenced API key vars* | — | When in JSON mode, each endpoint's `api_key_env` field names an env var that holds the actual key (e.g. `OPENAI_KEY_PRIMARY`). Those vars must be present in the completion-service process. |
| `SERVE_PORT` | `50053` | gRPC listen port |

#### Completion pool JSON (multi-endpoint mode)

Full schema (see [`config/pool.example.json`](config/pool.example.json) for a populated example):

```jsonc
{
  "strategy":     "weighted_random",   // weighted_random | least_pending | ewma_latency
  "max_attempts": 3,                   // retry budget per request; pool tries up to this many endpoints
  "breaker": {                         // optional; omit or "enabled": false to disable circuit breaking
    "enabled":       true,
    "max_requests":  1,                // half-open trial requests allowed at once
    "interval":      "60s",            // closed-state sliding window for rolling counters
    "timeout":       "30s",            // open-state cooldown before trying half-open
    "failure_ratio": 0.5,              // trip when failures / requests >= this (within `interval`)
    "min_requests":  5                 // minimum requests in window before ratio is evaluated
  },
  "endpoints": [
    {
      "name":        "openai-primary",                                  // unique id used in admin API / stats
      "url":         "https://api.openai.com/v1/chat/completions",      // full chat completions URL
      "api_key_env": "OPENAI_KEY_PRIMARY",                              // env var name; not the key itself
      "weight":      3,                                                 // > 0; relative pick probability under weighted_random
      "models":      ["gpt-4o", "gpt-4o-mini"],                         // optional; empty or ["*"] = any model
      "enabled":     true                                               // false → selectors skip it; admin can toggle live
    }
  ]
}
```

**Strategy semantics**
| Strategy | Behaviour |
|---|---|
| `weighted_random` | Pick proportional to `weight`. Stateless and fair. Default if unspecified. |
| `least_pending` | Pick endpoint with the lowest `in_flight` request count. Good when LLM latencies are highly skewed by load. |
| `ewma_latency` | Pick endpoint with the lowest EWMA latency (alpha=0.2). Zero-sample endpoints get a one-shot probe boost to avoid cold-start starvation. |

**Filters applied before each pick** (always on, in order): `model_affinity` (skip endpoints whose `models` list doesn't include the request's model; `["*"]` or empty = accept anything) → `breaker_open` (skip endpoints whose circuit breaker is in the open state).

**Retry semantics**: the pool retries on **synchronous** errors from the underlying upstream (non-2xx, dial failure, etc.). Once a streaming channel has been returned to the caller, mid-stream errors are surfaced as-is and not retried — this is a deliberate trade-off to keep first-byte latency low. See `completion/pool/pool.go:callEndpoint` for the exact boundary.

**Runtime mutation**: every field above can be changed at runtime via the admin API (`/admin/completion/endpoint*`) — see [`docs/api.md` § 3.3](docs/api.md#33-completion-上游池管理). Note that changes affect **only the receiving replica's in-memory state**; in a multi-replica deployment, either call each replica or restart all replicas to pick up the persistent file.

### RAG Service (`rag-service`)

| Variable | Default | Description |
|----------|---------|-------------|
| `EMBED_ADDR` | `localhost:50051` | Embedding service gRPC address |
| `QDRANT_HOST` | `localhost` | Qdrant hostname |
| `QDRANT_PORT` | `6334` | Qdrant gRPC port |
| `QDRANT_COLLECTION_NAME` | `llm_rag_documents` | Qdrant collection for RAG chunks (separate from the cache collection) |
| `RAG_SIMILARITY_THRESHOLD` | `0.6` | Minimum cosine similarity for a chunk to be retrieved. Set to `0` to disable score filtering entirely (every chunk passing the collection filter is returned, up to `RAG_DEFAULT_TOP_K`) |
| `RAG_DEFAULT_TOP_K` | `3` | Maximum number of chunks returned per query |
| `SERVE_PORT` | `50055` | gRPC listen port |

### Auth Service (`auth-service`)

| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_ADDR` | — | **Required.** Redis address (`host:port`) |
| `REDIS_PASSWORD` | `""` | Redis password (leave empty if none) |
| `REDIS_DB` | — | **Required.** Redis database index |
| `SERVE_PORT` | `50054` | gRPC listen port |

*`REDIS_ADDR` points at the redis container in the default docker compose file.*

### Service discovery (etcd) — applies to all services

When `ETCD_ENDPOINTS` is set, every service registers itself under `services/<name>/<instance-id>` with a 10-second lease, and clients resolve peers via `etcd:///services/<name>` with gRPC `round_robin` balancing. When `ETCD_ENDPOINTS` is empty, the gateway falls back to direct dialing of `*_ADDR` (single-instance mode); use this only for local development.

| Variable | Default | Description |
|---|---|---|
| `ETCD_ENDPOINTS` | `""` | Comma-separated etcd endpoints (e.g. `etcd:2379` or `etcd1:2379,etcd2:2379`). Empty → discovery off. |
| `ADVERTISE_ADDR` | `<hostname>:<SERVE_PORT>` | Per-instance advertise address registered in etcd. Default works for `docker compose --scale` and k8s. Override only when the container hostname is unreachable by peers. |
| `EMBEDDING_ADVERTISE_ADDR` / `CACHE_ADVERTISE_ADDR` / `COMPLETION_ADVERTISE_ADDR` / `AUTH_ADVERTISE_ADDR` / `RAG_ADVERTISE_ADDR` | — | Per-service overrides — useful in compose when one wrapper env wants to set advertise per service. The service-local `ADVERTISE_ADDR` (set via docker-compose `environment:`) is what each container actually reads. |

For a worked horizontal-scaling example see [`config/docker-compose.scale.example.yml`](config/docker-compose.scale.example.yml). End-to-end behaviour (lease eviction, gateway failover) is exercised by `test/cli/etcd-e2e-test.sh`.

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

**Option A — Raw text (recommended):** POST plain text or Markdown. The gateway auto-chunks it in the background and returns immediately.

```sh
curl -X POST http://localhost:8081/admin/rag/ingest/text \
     -H "X-Admin-Secret: your-secret" \
     -H "Content-Type: application/json" \
     -d '{
       "collection":    "alice",
       "source":        "docs/product-faq.md",
       "text":          "# Refund Policy\n\nOur refund policy is 30 days...\n\nTo request a refund, email support@example.com.\n\nRefunds are processed within 5 business days.",
       "chunk_size":    500,
       "chunk_overlap": 50
     }'
# Response (202 Accepted, immediate):
# {"job_id":"<uuid>","collection":"alice","status":"queued","chunk_count":3}
```

The `chunk_size` (default 500) and `chunk_overlap` (default 50) fields are optional. Chunking is Markdown-aware: headings always start a new chunk, paragraphs are merged up to `chunk_size` runes, and sentence boundaries are preferred for splits. The actual embedding + vector storage happens asynchronously in the background.

**Option B — Pre-chunked (advanced):** Supply chunks you have already split yourself. Ingestion is synchronous and returns only after all chunks are stored.

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

### Debugging retrieval

If RAG context never seems to appear in the LLM's answers, set `LOG_LEVEL=DEBUG` on the gateway. Each request will emit:

- `RAG: resolved collection="<name>" from <source>` — the collection name the gateway will query, plus its source (`X-RAG-Collection header` or `Auth.Subject (token alias)`).
- `RAG: retrieve returned 0 chunks for collection="<name>"` — emitted when the query found nothing. The most common cause is a mismatch between the `collection` field used at ingest time and the name resolved at query time.

When the gateway has RAG configured but cannot resolve any collection (no `X-RAG-Collection` header **and** no token alias), it emits a warning and skips retrieval.

Worked example: if you ingested with `collection: "mr"` but the request's token alias is `test_token`, the gateway will look up collection `test_token` and find nothing. Either pass `-H "X-RAG-Collection: mr"` on the request, or re-ingest using the token alias as the collection name.

To verify with no score filtering at all, restart `rag-service` with `RAG_SIMILARITY_THRESHOLD=0`. The startup log will show `threshold=0.0000` and the qdrant query will skip the score filter entirely — useful to confirm the collection filter is the actual blocker.

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
| `POST` | `/admin/rag/ingest/text` | `{"collection","source","text","chunk_size","chunk_overlap"}` | Ingest raw text/Markdown — auto-chunked, **async** (202) |
| `POST` | `/admin/rag/ingest` | `{"collection","source","chunks":[...]}` | Ingest pre-chunked content, synchronous |
| `DELETE` | `/admin/rag/doc` | `{"doc_id","collection"}` | Delete all chunks of a document |

### Gateway admin configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `ADMIN_SECRET` | — | **Required.** Shared secret for `X-Admin-Secret` header. Admin API is disabled when empty. |

## Service Discovery (etcd)

Each microservice can optionally register itself with etcd on startup, and every gRPC client in the gateway resolves peers through `etcd:///services/<name>` with `round_robin` load balancing. This is what enables horizontal scaling — e.g. `docker compose up -d --scale embedding-service=3`.

### Modes

| `ETCD_ENDPOINTS` | Behavior |
|---|---|
| **unset / empty** | Each service ignores etcd; clients dial the legacy `*_ADDR` (`EMBED_ADDR`, `CACHE_ADDR`, …) directly. Single-instance mode, identical to pre-discovery behavior. |
| **set** (e.g. `etcd:2379`) | Services register with a 10-second lease and keep it alive while running; clients watch `services/<name>/*` and balance across the live set. |

### Per-instance address

When a service registers, it advertises an `<host>:<port>` to peers. Resolution order:

1. `ADVERTISE_ADDR` env var (explicit override)
2. `os.Hostname() + ":" + SERVE_PORT` (default — matches the container hostname under compose/k8s)

### Graceful shutdown

On `SIGTERM` / `SIGINT`, each registered service:

1. Marks its `grpc.health.v1` health endpoint `NOT_SERVING`
2. Calls `mgr.DeleteEndpoint` and `lease.Revoke` so the etcd key disappears immediately (no need to wait for TTL)
3. Calls `grpc.Server.GracefulStop()` to drain in-flight RPCs

If the process is hard-killed (SIGKILL), the lease expires after 10 s and the endpoint is evicted automatically.

### Health checks

Every registered service exposes the standard `grpc.health.v1.Health` service alongside its business gRPC. External probes (Kubernetes liveness, sidecars) can call it via `grpc_health_probe -addr=<host>:<port>`.

### Scope

All five gRPC services (`embedding`, `cache`, `completion`, `auth`, `rag`) register with etcd on startup and resolve their peers through it. The gateway is a pure client and reaches every service via `etcd:///services/<name>`.

### Scaling a service

Because the compose file now uses `expose:` (in-network only) instead of host port mappings for the gRPC services, you can scale any of them directly:

```sh
docker compose -f docker-compose.prod.yml up -d --scale embedding-service=3 --scale cache-service=2
```

Verify with:

```sh
etcdctl --endpoints=localhost:2379 get --prefix services/
```

You should see one entry per running instance. Killing or stopping an instance evicts its entry; clients pick up the new set automatically.

## License

See [LICENSE](LICENSE).

