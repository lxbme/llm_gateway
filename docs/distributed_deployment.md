# Distributed Deployment Guide

[English](distributed_deployment.md) | [中文](distributed_deployment_zh_cn.md)

| Field | Value |
|---|---|
| **Status** | Stable |
| **Audience** | Operators deploying llm-gateway across multiple hosts |
| **Scope** | etcd-based service discovery, cross-host configuration, network requirements |
| **See Also** | [`pool_config.md`](pool_config.md), [`api.md`](api.md), [`gateway_working_mechanism.md`](gateway_working_mechanism.md) |

---

## 1. Introduction

### 1.1 Synopsis

This document describes how to deploy the llm-gateway microservice suite — `gateway`, `embedding-service`, `cache-service`, `completion-service`, `auth-service`, and `rag-service` — across multiple hosts. It supersedes the single-host Compose layout found in `docker-compose.prod.yml` for production-class deployments.

The single-host Compose stack relies on Docker's internal bridge network for service-to-service DNS. In a multi-host topology that network is no longer available; service discovery must be delegated to an `etcd` cluster, and each instance must advertise an address reachable by its peers on the LAN.

### 1.2 Document conventions

The following conventions apply throughout this document:

- **Directive** — an environment variable consumed by one or more services.
- **Required** — whether the directive must be set for the described topology.
- **Default** — the value applied when the directive is unset.
- **Scope** — the set of services that read the directive.
- Hosts are referenced as `host-a`, `host-b`, etc.; LAN addresses are illustrative (`10.0.1.10` ... `10.0.1.13`).
- Code blocks use POSIX shell or YAML syntax. Mandatory placeholders are written `<like-this>`.

### 1.3 Prerequisites

- A LAN (or virtual overlay network) reachable by every participating host on the ports listed in Section 5.
- Container runtime (Docker or Podman) installed on every host that runs microservices.
- One reachable upstream LLM endpoint per provider configured in the completion pool (see [`pool_config.md`](pool_config.md)).
- A shared secret store mechanism (environment file, secrets manager, or orchestrator-native secret) for API keys and `ADMIN_SECRET`.

---

## 2. Architecture overview

### 2.1 Service components

| Component | Role | Default port | Stateful |
|---|---|---|---|
| `gateway` | HTTP entry point; aggregates auth, cache, RAG, completion | 8080 (public), 8081 (admin) | No |
| `embedding-service` | Text embedding (proxies upstream embedding provider) | 50051 | No |
| `cache-service` | Semantic cache lookup and write-back | 50052 | No (uses Qdrant) |
| `completion-service` | Upstream LLM pool, streaming, retry, circuit breaking | 50053 | No (in-process pool state) |
| `auth-service` | API key issuance, validation, rate limit accounting | 50054 | No (uses Redis) |
| `rag-service` | Document retrieval, ingestion | 50055 | No (uses Qdrant) |
| `qdrant` (third party) | Vector store backing cache and RAG | 6333 (HTTP), 6334 (gRPC) | Yes |
| `redis` (third party) | Persistent key-value store for auth | 6379 | Yes |
| `etcd` (third party) | Service registry and configuration coordination | 2379 (client), 2380 (peer) | Yes |

Only `qdrant`, `redis`, and `etcd` hold persistent state. The six llm-gateway microservices are stateless processes and may be restarted, scaled, or relocated without data loss.

### 2.2 Service discovery model

The registration and resolution mechanism is implemented in `internal/discovery/`. Two modes are supported:

1. **Discovery enabled** — `ETCD_ENDPOINTS` is set. Each service writes a key `services/<service-name>/<instance-id>` carrying its `ADVERTISE_ADDR`, refreshed by a 10-second lease keepalive. Clients use the gRPC resolver `etcd:///services/<service-name>` with `round_robin` load balancing.
2. **Direct dialing** — `ETCD_ENDPOINTS` is unset. The gateway dials each backend via the corresponding `*_ADDR` variable (`CACHE_ADDR`, `COMPL_ADDR`, ...). This mode is intended for local development on a single host.

Multi-host deployments MUST operate in discovery-enabled mode.

### 2.3 Address advertisement

When discovery is enabled, the address each instance registers is determined by `internal/discovery/register.go`:

```
ADVERTISE_ADDR (env var) → if set, used verbatim
                         → otherwise os.Hostname() + ":" + <fallback-port>
```

The hostname fallback works inside a single Docker bridge network because the bridge's embedded DNS resolves container names. Across hosts the container hostname is not resolvable; `ADVERTISE_ADDR` MUST be set explicitly to a LAN-routable `host:port` value.

A misconfigured `ADVERTISE_ADDR` is the most common cause of inter-service connection failures in multi-host topologies. Verify the value with:

```bash
etcdctl --endpoints=<etcd-host>:2379 get /services/ --prefix
```

Every value returned must be reachable from the gateway host.

---

## 3. Deployment topologies

### 3.1 Single-host (reference)

The repository ships `docker-compose.prod.yml` for a self-contained single-host deployment. It is provided as a reference for local validation and demonstration; it is not the subject of this document.

### 3.2 Distributed

A distributed deployment partitions the suite across at least three host roles:

| Tier | Components | Sample host count |
|---|---|---|
| **Edge** | `gateway` × N, ingress load balancer (nginx, HAProxy, cloud LB) | 1–N |
| **Stateless compute** | `embedding-service`, `cache-service`, `completion-service`, `auth-service`, `rag-service` | 1–M per service |
| **Data** | `etcd` cluster (3 or 5 nodes), `qdrant`, `redis` | 1–N |

The stateless compute tier may be further sharded by service if request volumes differ substantially; for example, a completion-heavy workload may run 3× `completion-service` and 1× of each other service.

### 3.3 Hybrid

For evaluation environments, individual tiers may be collapsed. A minimal three-host distributed deployment is provided in Section 6.

---

## 4. Configuration reference

### 4.1 Common directives

These directives are read by every llm-gateway microservice.

#### `ETCD_ENDPOINTS`

| Field | Value |
|---|---|
| **Description** | Comma-separated list of etcd client endpoints (`host:port`). Enables service discovery when non-empty. |
| **Required** | Yes, for any multi-host deployment. |
| **Default** | *(unset)* — discovery disabled, direct dialing via `*_ADDR` only. |
| **Scope** | All services. |
| **Example** | `ETCD_ENDPOINTS=10.0.1.10:2379,10.0.1.11:2379,10.0.1.12:2379` |

For production deployments, list every etcd cluster node so that client connections survive the loss of any single node.

#### `ADVERTISE_ADDR`

| Field | Value |
|---|---|
| **Description** | The `host:port` value the instance registers with etcd. Other services connect to this address. |
| **Required** | Yes, for any multi-host deployment. |
| **Default** | `os.Hostname() + ":" + <fallback-port>` — valid only inside a Docker bridge network. |
| **Scope** | All services. |
| **Example** | `ADVERTISE_ADDR=10.0.1.11:50053` (completion-service on host-b) |

The value MUST be reachable from every host that runs `gateway`. `127.0.0.1` is never valid; container-internal hostnames are not valid across hosts.

The per-service aliases `EMBEDDING_ADVERTISE_ADDR`, `CACHE_ADVERTISE_ADDR`, `COMPLETION_ADVERTISE_ADDR`, `AUTH_ADVERTISE_ADDR`, `RAG_ADVERTISE_ADDR` are recognised by `docker-compose.prod.yml` as wrapper variables; inside each container the canonical name remains `ADVERTISE_ADDR`.

#### `SERVE_PORT`

| Field | Value |
|---|---|
| **Description** | Port the in-process gRPC (or HTTP, for `gateway`) server binds to. |
| **Required** | No |
| **Default** | Per-service default — see Section 2.1. |
| **Scope** | All services. |
| **Example** | `SERVE_PORT=50063` (used to colocate two completion-service instances on one host) |

When multiple instances of the same service are colocated on one host with `network_mode: host`, distinct `SERVE_PORT` values must be assigned, and `ADVERTISE_ADDR` must use the same port.

### 4.2 `gateway`

| Directive | Required | Description |
|---|---|---|
| `ETCD_ENDPOINTS` | Yes | See Section 4.1. |
| `ADMIN_SECRET` | Yes | Shared secret required by `X-Admin-Secret` header on `/admin/*` routes. |
| `DEBUG_MODE` | No | Enables verbose request logging. Default `false`. |
| `LOG_LEVEL` | No | One of `ERROR`, `WARN`, `INFO`, `DEBUG`. Default `ERROR`. |
| `CACHE_ADDR`, `COMPL_ADDR`, `AUTH_ADDR`, `RAG_ADDR` | No | Direct-dial fallback addresses. Ignored when `ETCD_ENDPOINTS` is set. |

The gateway is the only service that should be exposed beyond the data-plane LAN. The admin port (8081) MUST NOT be exposed beyond `127.0.0.1`; remote administration is performed via SSH-tunneled access (see Section 7.4).

### 4.3 `embedding-service`

| Directive | Required | Description |
|---|---|---|
| `ETCD_ENDPOINTS` | Yes | See Section 4.1. |
| `ADVERTISE_ADDR` | Yes | LAN-reachable `host:50051` (or other `SERVE_PORT`). |
| `EMBED_PROVIDER` | Yes | Upstream provider identifier (e.g. `openai`). |
| `EMBED_API_KEY` | Yes | Provider API key. |
| `EMBED_ENDPOINT` | Yes | Provider HTTPS endpoint URL. |
| `EMBED_MODEL` | Yes | Model name. |
| `EMBED_DIMENSIONS` | Yes | Embedding dimensionality, integer. |

### 4.4 `cache-service`

| Directive | Required | Description |
|---|---|---|
| `ETCD_ENDPOINTS` | Yes | See Section 4.1. |
| `ADVERTISE_ADDR` | Yes | LAN-reachable `host:50052`. |
| `QDRANT_HOST` | Yes | Hostname or IP of the Qdrant instance. |
| `QDRANT_PORT` | No | Default `6334` (gRPC). |
| `QDRANT_COLLECTION_NAME` | No | Default `llm_semantic_cache`. |
| `QDRANT_SIMILARITY_THRESHOLD` | No | Float in `[0,1]`. Default `0.95`. |
| `CACHE_MODE` | No | `semantic` or `exact`. Default `semantic`. |
| `CACHE_STORE_PROVIDER` | No | Default `qdrant`. |
| `CACHE_BUFFER_SIZE` | No | Default `1000`. |
| `CACHE_WORKER_COUNT` | No | Default `5`. |

In multi-host mode the directive `EMBED_ADDR` is not consulted; the embedding service is resolved via etcd.

### 4.5 `completion-service`

| Directive | Required | Description |
|---|---|---|
| `ETCD_ENDPOINTS` | Yes | See Section 4.1. |
| `ADVERTISE_ADDR` | Yes | LAN-reachable `host:50053`. |
| `COMPL_POOL_CONFIG_FILE` | Conditional | Path to a JSON file inside the container describing the upstream pool. See [`pool_config.md`](pool_config.md). |
| `COMPL_POOL_CONFIG` | Conditional | Inline JSON, used when `COMPL_POOL_CONFIG_FILE` is unset. |
| `COMPL_ENDPOINT`, `COMPL_API_KEY` | Conditional | Legacy single-endpoint mode; used when neither pool directive is set. Deprecated in distributed deployments. |
| *(provider-specific key envs)* | Yes | The values of `api_key_env` fields referenced from the pool JSON. |

The pool configuration is read at process start. Runtime mutation via the admin API (Section 7.4 and [`api.md`](api.md) § 3.3) affects only the instance receiving the request; see Section 8.2.

### 4.6 `auth-service`

| Directive | Required | Description |
|---|---|---|
| `ETCD_ENDPOINTS` | Yes | See Section 4.1. |
| `ADVERTISE_ADDR` | Yes | LAN-reachable `host:50054`. |
| `REDIS_ADDR` | Yes | `host:port` of the Redis instance. |
| `REDIS_DB` | No | Default `0`. |

### 4.7 `rag-service`

| Directive | Required | Description |
|---|---|---|
| `ETCD_ENDPOINTS` | Yes | See Section 4.1. |
| `ADVERTISE_ADDR` | Yes | LAN-reachable `host:50055`. |
| `QDRANT_HOST` | Yes | Hostname or IP of the Qdrant instance. |
| `QDRANT_PORT` | No | Default `6334`. |
| `QDRANT_COLLECTION_NAME` | No | Default `llm_rag_documents`. |
| `RAG_SIMILARITY_THRESHOLD` | No | Default `0.6`. |
| `RAG_DEFAULT_TOP_K` | No | Default `3`. |

The embedding dependency is resolved via etcd; no `EMBED_ADDR` is required.

---

## 5. Network requirements

### 5.1 Port map

| Component | Listen port | Protocol | Reached by |
|---|---|---|---|
| `etcd` | 2379 | HTTP (client) | All llm-gateway services |
| `etcd` | 2380 | HTTP (peer) | Other etcd nodes |
| `qdrant` | 6334 | gRPC | `cache-service`, `rag-service` |
| `redis` | 6379 | RESP | `auth-service` |
| `embedding-service` | 50051 | gRPC | Other llm-gateway services |
| `cache-service` | 50052 | gRPC | `gateway` |
| `completion-service` | 50053 | gRPC | `gateway` |
| `auth-service` | 50054 | gRPC | `gateway` |
| `rag-service` | 50055 | gRPC | `gateway` |
| `gateway` | 8080 | HTTP | Ingress load balancer |
| `gateway` | 8081 | HTTP (admin) | `127.0.0.1` only |

Ports listed for llm-gateway services apply per instance. When multiple instances of the same service are colocated, additional ports must be allocated as described in Section 4.1 (`SERVE_PORT`).

### 5.2 Firewall rules

The minimum inbound ruleset for each host role is:

- **etcd hosts** — allow 2379/tcp from all llm-gateway hosts; 2380/tcp from peer etcd hosts only.
- **Qdrant host** — allow 6334/tcp from `cache-service` and `rag-service` hosts.
- **Redis host** — allow 6379/tcp from `auth-service` hosts.
- **Microservice hosts** — allow the relevant service ports (Section 5.1) from every `gateway` host.
- **Gateway hosts** — allow 8080/tcp from the ingress load balancer.
- **Ingress** — allow 80/tcp and 443/tcp from the public Internet (or wherever clients originate).

Administrative access to port 8081 of any `gateway` host MUST be limited to localhost. Remote administration uses SSH local forwarding, as described in Section 7.4.

### 5.3 Address advertisement rules

1. `ADVERTISE_ADDR` MUST resolve from every consumer host.
2. `ADVERTISE_ADDR` MUST NOT be `127.0.0.1`, `localhost`, or any address bound to a network namespace not shared with consumers.
3. When `network_mode: host` is used, `ADVERTISE_ADDR` MUST match the host IP and the listening `SERVE_PORT`.
4. When Docker bridge networking with explicit port publishing is used, `ADVERTISE_ADDR` MUST reflect the published host port, not the container-internal port.

---

## 6. Worked example: four-host distributed deployment

This example documents a topology spanning four hosts:

| Host | Address | Components |
|---|---|---|
| host-a | `10.0.1.10` | `etcd-1`, `qdrant`, `redis` |
| host-b | `10.0.1.11` | `etcd-2`, `embedding-service`, `completion-service` × 2 |
| host-c | `10.0.1.12` | `etcd-3`, `cache-service`, `rag-service`, `auth-service` |
| host-d | `10.0.1.13` | `gateway` × 2, `nginx` |

### 6.1 etcd cluster bootstrap

On host-a:

```yaml
services:
  etcd-1:
    image: quay.io/coreos/etcd:v3.5.17
    network_mode: host
    command:
      - /usr/local/bin/etcd
      - --name=etcd-1
      - --advertise-client-urls=http://10.0.1.10:2379
      - --listen-client-urls=http://0.0.0.0:2379
      - --initial-advertise-peer-urls=http://10.0.1.10:2380
      - --listen-peer-urls=http://0.0.0.0:2380
      - --initial-cluster=etcd-1=http://10.0.1.10:2380,etcd-2=http://10.0.1.11:2380,etcd-3=http://10.0.1.12:2380
      - --initial-cluster-state=new
      - --initial-cluster-token=llm-gateway-etcd
```

Repeat on host-b and host-c with `--name` and the corresponding peer URLs adjusted. All three nodes must be started within the election timeout window of each other on first boot.

### 6.2 Stateless services on host-b

```yaml
services:
  embedding-service:
    image: ghcr.io/lxbme/llm-gateway-embedding-service:0.4.0
    network_mode: host
    environment:
      - ETCD_ENDPOINTS=10.0.1.10:2379,10.0.1.11:2379,10.0.1.12:2379
      - ADVERTISE_ADDR=10.0.1.11:50051
      - EMBED_PROVIDER=openai
      - EMBED_API_KEY=${EMBED_API_KEY}
      - EMBED_ENDPOINT=https://api.openai.com/v1/embeddings
      - EMBED_MODEL=text-embedding-3-small
      - EMBED_DIMENSIONS=1536

  completion-service-0:
    image: ghcr.io/lxbme/llm-gateway-completion-service:0.4.0
    network_mode: host
    environment:
      - ETCD_ENDPOINTS=10.0.1.10:2379,10.0.1.11:2379,10.0.1.12:2379
      - ADVERTISE_ADDR=10.0.1.11:50053
      - SERVE_PORT=50053
      - COMPL_POOL_CONFIG_FILE=/etc/llm/pool.json
      - OPENAI_KEY_PRIMARY=${OPENAI_KEY_PRIMARY}
      - AZ_KEY=${AZ_KEY}
    volumes:
      - /etc/llm/pool.json:/etc/llm/pool.json:ro

  completion-service-1:
    image: ghcr.io/lxbme/llm-gateway-completion-service:0.4.0
    network_mode: host
    environment:
      - ETCD_ENDPOINTS=10.0.1.10:2379,10.0.1.11:2379,10.0.1.12:2379
      - ADVERTISE_ADDR=10.0.1.11:50063
      - SERVE_PORT=50063
      - COMPL_POOL_CONFIG_FILE=/etc/llm/pool.json
      - OPENAI_KEY_PRIMARY=${OPENAI_KEY_PRIMARY}
      - AZ_KEY=${AZ_KEY}
    volumes:
      - /etc/llm/pool.json:/etc/llm/pool.json:ro
```

The two completion-service instances differ only in `ADVERTISE_ADDR` and `SERVE_PORT`. Both register under `services/completion-service/` and are load-balanced by the gateway's `round_robin` policy.

### 6.3 Gateway and ingress on host-d

```yaml
services:
  gateway-0:
    image: ghcr.io/lxbme/llm-gateway-gateway:0.4.0
    network_mode: host
    environment:
      - SERVE_PORT=8080
      - ETCD_ENDPOINTS=10.0.1.10:2379,10.0.1.11:2379,10.0.1.12:2379
      - LOG_LEVEL=INFO
      - ADMIN_SECRET=${ADMIN_SECRET}

  gateway-1:
    image: ghcr.io/lxbme/llm-gateway-gateway:0.4.0
    network_mode: host
    environment:
      - SERVE_PORT=8090
      - ETCD_ENDPOINTS=10.0.1.10:2379,10.0.1.11:2379,10.0.1.12:2379
      - LOG_LEVEL=INFO
      - ADMIN_SECRET=${ADMIN_SECRET}

  nginx:
    image: nginx:alpine
    network_mode: host
    volumes:
      - ./nginx.conf:/etc/nginx/nginx.conf:ro
```

A minimal `nginx.conf` upstream block:

```nginx
upstream llm_gateway {
    server 127.0.0.1:8080;
    server 127.0.0.1:8090;
}

server {
    listen 443 ssl http2;
    server_name api.example.com;
    ssl_certificate     /etc/letsencrypt/live/api.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/api.example.com/privkey.pem;

    proxy_buffering    off;
    proxy_http_version 1.1;
    proxy_read_timeout 300s;

    location / {
        proxy_pass http://llm_gateway;
        proxy_set_header X-Forwarded-For $remote_addr;
    }
}
```

`proxy_buffering off` is mandatory; otherwise streaming completion responses are buffered until upstream EOF.

---

## 7. Operational procedures

### 7.1 Bootstrap order

1. Start the etcd cluster on host-a, host-b, host-c. Verify with `etcdctl --endpoints=... endpoint health` reporting healthy on all three.
2. Start `qdrant` on host-a; confirm `curl http://10.0.1.10:6333/healthz` returns OK.
3. Start `redis` on host-a; confirm `redis-cli -h 10.0.1.10 ping` returns `PONG`.
4. Start the stateless services. Order between them does not matter — etcd absorbs registrations in arbitrary order.
5. Verify discovery: `etcdctl get /services/ --prefix` should return one key per running instance, each value a LAN-reachable `host:port`.
6. Start `gateway` instances on host-d.
7. Start `nginx`.
8. Issue a probe request: `curl https://api.example.com/v1/health`.

### 7.2 Verification

End-to-end verification of a distributed deployment:

```bash
# 1. Confirm all services registered
etcdctl --endpoints=10.0.1.10:2379 get /services/ --prefix --keys-only

# 2. Confirm gateway can reach each backend
curl -sS https://api.example.com/v1/health

# 3. Issue a non-trivial completion request
curl -sS https://api.example.com/v1/chat/completions \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"ping"}]}'

# 4. Inspect pool stats on a specific gateway instance
ssh host-d -L 8081:127.0.0.1:8081 -N &
curl -H "X-Admin-Secret: $ADMIN_SECRET" http://127.0.0.1:8081/admin/completion/stats | jq
```

### 7.3 Rolling restart

To restart a stateless service without dropping requests:

1. Stop the targeted instance with `SIGTERM`. The service's deregister hook (`internal/discovery/register.go`) actively removes its etcd key, so the gateway stops directing new requests to it within one resolver tick.
2. Wait for in-flight requests to drain (typically a few seconds for non-streaming, longer for streaming completions).
3. Start the replacement instance with the same `ADVERTISE_ADDR`. Etcd re-registration is immediate; the gateway picks up the new address on the next resolver tick.

If `SIGTERM` is not honoured (process killed `-9`), the etcd lease expires after 10 seconds (`leaseTTLSeconds` in `internal/discovery/register.go`); during that window the gateway may route to the dead address and observe connection failures.

### 7.4 Remote administration

The gateway admin port (8081) is bound to `127.0.0.1` on every gateway host. Remote administration is performed via SSH local forwarding:

```bash
ssh -L 8081:127.0.0.1:8081 -N operator@host-d
# In a second terminal:
curl -H "X-Admin-Secret: $ADMIN_SECRET" http://127.0.0.1:8081/admin/completion/endpoints
```

Admin operations affecting the completion pool (add / remove / reweight / disable / breaker reset) modify only the instance receiving the request. See Section 8.2.

---

## 8. Known limitations

### 8.1 Inter-service traffic is unencrypted

gRPC connections between llm-gateway services use insecure credentials. This is acceptable on a trusted LAN. For deployments spanning untrusted networks (multi-region, public cloud across availability zones), tunnel inter-service traffic over WireGuard, IPsec, or a service mesh sidecar. Native TLS is not implemented at this revision.

### 8.2 Admin mutations are instance-local

Runtime modifications to the completion pool propagate only to the specific `completion-service` instance that received the admin RPC. There is no broadcast mechanism. To apply changes uniformly:

- Update `COMPL_POOL_CONFIG_FILE` on every host, then perform a rolling restart (Section 7.3); or
- Iterate over every instance using direct addresses, bypassing the etcd `round_robin` policy.

### 8.3 Stateful dependencies are single points of failure

The example topology uses single-node `qdrant` and `redis`. For high availability, replace these with clustered or managed equivalents (Redis Sentinel/Cluster, Qdrant Cloud, or a self-hosted Qdrant cluster).

### 8.4 Lease expiry window

When an instance fails without graceful shutdown, its etcd key persists until the 10-second lease expires. During that window, the gateway's `round_robin` balancer may attempt the dead address. Client retries are recommended for production callers.

---

## 9. See also

- [`pool_config.md`](pool_config.md) — Completion pool configuration reference.
- [`api.md`](api.md) — HTTP API reference, including admin endpoints.
- [`gateway_working_mechanism.md`](gateway_working_mechanism.md) — Request flow inside the gateway.
- [`../config/docker-compose.scale.example.yml`](../config/docker-compose.scale.example.yml) — Worked Compose example for single-host horizontal scaling.
- [`../README.md`](../README.md) — Project overview, quick start, configuration index.
