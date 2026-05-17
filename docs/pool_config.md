# Completion Pool Configuration Reference

[English](pool_config.md) | [中文](pool_config_zh_cn.md)

This document is the canonical reference for the `completion-service` upstream pool — the layer that decides which upstream LLM endpoint each request goes to, how to retry, when to circuit-break, and how to surface runtime stats.

For a quick orientation, see the inline summary in the project [`README.md`](../README.md#completion-pool-json-multi-endpoint-mode). For the on-the-wire admin HTTP API used to mutate the pool at runtime, see [`docs/api.md` § 3.3](api.md#33-completion-上游池管理).

---

## 1. Why a pool?

`completion-service` historically had a single hardcoded upstream (`COMPL_ENDPOINT`). The pool layer adds, behind the same gRPC interface:

- **Multiple upstreams** — multiple OpenAI-compatible endpoints (OpenAI, Azure OpenAI, self-hosted, mixed providers) selectable per request
- **Weighted distribution** — quotas allocated by weight or by realtime metrics
- **Model affinity** — route per-model requests to providers that actually serve that model
- **Circuit breaking** — auto-skip endpoints whose failure rate exceeds a threshold; auto-recover via half-open trials
- **Pre-first-byte retry** — on synchronous errors (dial / non-2xx / handshake) the pool transparently retries on another endpoint, all before the client sees any bytes
- **Live mutation** — admin API can add/remove/reweight/disable endpoints with no restart

Crucially, this is **orthogonal to horizontal scaling**: etcd discovery spreads external load across N `completion-service` replicas, and within each replica the pool spreads work across M upstream endpoints. The two axes compose freely.

---

## 2. Configuration sources

`completion-service` reads its pool config from exactly one of three sources, in priority order:

| Priority | Source | Env var(s) | Notes |
|---|---|---|---|
| 1 (highest) | JSON file | `COMPL_POOL_CONFIG_FILE` | Path to a JSON file (absolute or relative to the working dir). Reload requires restart. |
| 2 | Inline JSON | `COMPL_POOL_CONFIG` | A JSON string in an env var. Useful in compose where you don't want to mount a file. |
| 3 (lowest) | Legacy single endpoint | `COMPL_ENDPOINT` + `COMPL_API_KEY` | Synthesises a single-endpoint pool; logs a deprecation warning at startup. |

If none of the above are set, the service exits with an explicit error at startup.

Example boot logs:

```
[Info] pool: strategy=weighted_random max_attempts=3 endpoints=[openai-primary(w=3,enabled=true,breaker=closed) azure-fallback(w=1,enabled=true,breaker=closed)]
```

```
[Info] pool: COMPL_POOL_CONFIG_FILE/COMPL_POOL_CONFIG not set, falling back to legacy single-endpoint mode (COMPL_ENDPOINT)
[Info] pool: strategy=weighted_random max_attempts=1 endpoints=[legacy(w=1,enabled=true,breaker=disabled)]
```

The loader runs in `completion/pool/config.go:LoadConfigFromEnv()`.

---

## 3. Top-level schema

```jsonc
{
  "strategy":     "weighted_random",   // see § 4
  "max_attempts": 3,                   // see § 5
  "breaker":      { ... },             // optional; see § 7
  "endpoints":    [ ... ]              // required; see § 6
}
```

| Field | Type | Default | Description |
|---|---|---|---|
| `strategy` | string | `"weighted_random"` | Selector algorithm. Allowed: `weighted_random`, `least_pending`, `ewma_latency`. |
| `max_attempts` | int | `3` | Maximum endpoints the pool will try **per request**. Tried endpoints are not retried within the same request. |
| `breaker` | object | disabled | Circuit-breaker settings shared by all endpoints. |
| `endpoints` | array | — | **Required.** At least one entry; at least one must have `"enabled": true`. |

> The parser is strict (`json.Decoder` with `DisallowUnknownFields()`): any typo in a key name causes startup failure. JSON does not support comments — use a sidecar `.md` or `_README` field if you need annotations (and then remove them before shipping).

---

## 4. Selection strategies (`strategy`)

The strategy decides which endpoint serves a given request after filters narrow the candidate set.

### 4.1 `weighted_random` (default)

Each endpoint has a positive integer `weight`. Pick probability is `weight / Σ(weight of eligible endpoints)`. Stateless and fair.

Use it when:
- You want simple, predictable traffic splits ("90% provider A, 10% provider B")
- All upstreams have comparable latency profiles
- You're starting out and haven't measured anything

### 4.2 `least_pending`

Picks the endpoint with the smallest current in-flight request count (`InFlight` atomic counter, incremented at request start and decremented when the stream closes). Ties are broken by `weight` (higher wins), then by name (lexicographic, for determinism).

Use it when:
- Upstream latencies are highly skewed by transient load
- You want adaptive distribution without configuring latency targets

### 4.3 `ewma_latency`

Picks the endpoint with the lowest EWMA (exponentially weighted moving average) latency, alpha=0.2. Latency is measured from request start to channel close, in microseconds.

**Cold-start probe boost**: endpoints that have served zero requests so far (`LatencyUsEWMA == 0`) are picked **before** measured endpoints — this guarantees a fresh endpoint will be probed at least once instead of being starved by a low-latency incumbent forever.

Tie-break: lower latency → higher weight → name.

Use it when:
- You want to gravitate to the fastest live upstream
- Latency varies meaningfully between providers (e.g., different regions, models)

---

## 5. Retry semantics (`max_attempts`)

`max_attempts` caps how many endpoints a **single** request can try. The pool iterates:

```
for attempt in 1 .. max_attempts:
    if ctx canceled: return ctx.Err()
    candidates = apply_filters(snapshot)
    ep = selector.pick(candidates, excluding tried)
    if ep is nil:
        return error
    mark ep as tried
    ch, err = call_upstream(ep)
    if err is nil: return ch       # success — channel handed to caller
    record err
return wrapped "exhausted" error
```

Within a single request:
- An endpoint is tried **at most once**. After a failure, it's added to `tried` and excluded by subsequent picks in this loop iteration.
- A pre-stream error from upstream (dial failure, non-2xx, body read error before goroutine spawn) triggers retry on another endpoint.
- A streaming error (`chunk.Error` arriving mid-stream after the channel is already returned) is **not** retried. It is propagated as-is. See § 8 for why.

If `max_attempts` exceeds the number of eligible endpoints, the loop exits early when the selector returns "no more endpoints" rather than re-trying the same ones.

---

## 6. Endpoint schema (`endpoints[i]`)

```jsonc
{
  "name":        "openai-primary",
  "url":         "https://api.openai.com/v1/chat/completions",
  "api_key_env": "OPENAI_KEY_PRIMARY",
  "weight":      3,
  "models":      ["gpt-4o", "gpt-4o-mini"],
  "enabled":     true
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | ✅ | Unique identifier within the pool. Used in admin API, logs, stats output. Cannot be empty; must be unique. |
| `url` | string | ✅ | Full chat completions URL. Must be parseable by `net/url`. No path mangling — provide the exact upstream URL including any query params (Azure needs `?api-version=...`). |
| `api_key_env` | string | ✅ | **Name of an environment variable** holding the API key, not the key itself. The completion-service process must have this env var set; the openai client reads it on every request. This indirection keeps secrets out of the config file. |
| `weight` | int | ✅ | Must be `> 0`. Used by `weighted_random`; used as a tie-breaker by `least_pending` and `ewma_latency`. |
| `models` | array of strings | ❌ | If absent / empty / `["*"]`, the endpoint accepts any model. Otherwise, only requests whose `model` field exactly matches one of the listed values are routed here. Globs / regex are **not** supported. |
| `enabled` | bool | ✅ | When `false`, all selectors skip this endpoint. Stats/breaker state are preserved so admin can re-enable it without losing history. |

### Why `api_key_env` instead of `api_key`?

1. **No secrets in config files**: a `pool.json` can be committed to a config repo or mounted from a configmap without leaking keys.
2. **Per-process injection**: in Docker/k8s, env vars are the normal channel for secrets (Docker secrets, k8s Secret -> envFrom).
3. **Same key across endpoints**: if two endpoints share an OPENAI key, both can reference the same env var.

When a request hits this endpoint, the openai client does `os.Getenv(api_key_env)`. If the env var is missing or empty, the upstream call will fail with a 401, the pool will retry on another endpoint, and the failure will count towards the breaker. The loader **does not** validate env var presence at startup — that's intentional to keep boot fast and tolerant of secrets that arrive after process start.

---

## 7. Circuit breaker (`breaker`)

The breaker is backed by [`sony/gobreaker`](https://github.com/sony/gobreaker). One breaker instance per endpoint; state is **not** shared between endpoints. When disabled (the default), `breaker_state` reports `"disabled"` and all filter logic short-circuits to "pass".

```jsonc
"breaker": {
  "enabled":       true,
  "max_requests":  1,
  "interval":      "60s",
  "timeout":       "30s",
  "failure_ratio": 0.5,
  "min_requests":  5
}
```

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | bool | `false` | When `false`, no breaker is built. All `BreakerState` snapshots will read `"disabled"`. |
| `max_requests` | uint32 | `1` | Number of trial requests allowed through in the half-open state. The first success closes the breaker; the first failure re-opens it. |
| `interval` | duration string | `"60s"` | Sliding window for the rolling counters in the closed state. Counters reset at each interval boundary. Set to `"0"` to keep cumulative counts forever (rarely useful). |
| `timeout` | duration string | `"30s"` | Time the breaker stays in the open state before transitioning to half-open. |
| `failure_ratio` | float (0..1] | `0.5` | The breaker trips when `failures / requests >= failure_ratio` (evaluated each time a request finishes failed). |
| `min_requests` | uint32 | `5` | Failure ratio is **not** evaluated until at least this many requests have been recorded in the current interval. Prevents flapping on small samples. |

Durations use Go's `time.ParseDuration` syntax: `"500ms"`, `"30s"`, `"5m"`. An invalid duration string causes startup failure when `enabled: true`.

**State machine**:

```
closed ─(failures cross threshold)─► open
  ▲                                     │
  │       (any success in half-open)    │
  │                                     ▼
  └──────────────────────────────── half-open ─(any failure)─► open
```

**State change logs** (level Info):

```
[Info] pool: breaker "openai-primary" closed -> open
[Info] pool: breaker "openai-primary" open -> half-open
[Info] pool: breaker "openai-primary" half-open -> closed
```

**What counts as a failure?** Only synchronous errors from `upstreamClient.GetStream` — i.e., the same errors that drive retry. Mid-stream `chunk.Error` does **not** currently increment breaker counters; see § 8 for the rationale and § 11 for the open question.

### Manually resetting the breaker

Admin API: `POST /admin/completion/breaker/reset` with `{"name":"..."}`. The breaker is rebuilt with the same settings; counters and state go back to `closed`. See [`docs/api.md` § 3.3](api.md#33-completion-上游池管理).

---

## 8. Filter chain

Before each selection, candidates pass through filters in order:

1. **`model_affinity`** — drops endpoints whose `models` list doesn't accept the request's `model`. Empty list or `["*"]` matches everything. The chunk's `Model` field is the OpenAI request's `model` field, untouched.
2. **`breaker_open`** — drops endpoints whose breaker is in the `StateOpen` state. Half-open endpoints pass through (so trial requests can run).

If filters reduce the candidate list to empty, the selector returns "no eligible endpoint" and the pool's retry loop terminates with an error. Common causes:
- All endpoints disabled (admin disabled them, or initial config has `enabled: false`)
- All endpoints' breakers are open simultaneously (correlated failures)
- The request's model matches nothing — usually a config bug or a typo in the client-supplied `model` field

---

## 9. Error / retry boundary in the streaming path

The pool's retry boundary is **strictly before** the upstream channel is returned to the caller. Once `pool.GetStream` returns a channel, the gateway opens an SSE stream to the client and the client may already be reading bytes — at that point retrying would either duplicate output (if we re-emit from a fresh upstream) or stall (if we wait for retry to silently succeed).

In the upstream-side openai client (`completion/openai/openai.go`):

```
1.  http.Do(...)                    — synchronous, can return err → retry
2.  if status != 200: return err    — synchronous, can return err → retry
3.  spawn goroutine reading SSE
4.  return ch, nil                  — channel handed to caller
5.  goroutine: ch <- first chunk    — first byte to client; retry impossible
```

Errors at steps 1-2 propagate as `(nil, err)` from `pool.callEndpoint`; the retry loop tries another endpoint. Errors after step 4 arrive as `chunk.Error` on the returned channel and are surfaced verbatim to the client.

This is a deliberate trade-off:
- ✅ First-byte latency stays low — no extra round trip to "validate" the stream
- ❌ A 200-with-immediate-error response (rare in practice) becomes a non-retried error to the client

If your upstream provider has high rates of stream-mid failures, the open path is a Phase 5+ refinement to peek the first chunk synchronously before returning the channel.

---

## 10. Validation rules

The loader rejects on startup if any of the following hold:

- `endpoints` is empty
- Two endpoints share the same `name`
- Any endpoint has empty `name`, `url`, or `api_key_env`
- Any endpoint has a `url` that `net/url.Parse` rejects
- Any endpoint has `weight <= 0` (note: `weight` defaults to `1` if omitted entirely, but explicit `0` or negative is rejected)
- No endpoint has `enabled: true`
- `strategy` is not one of the three supported names
- `breaker.enabled: true` and `breaker.interval` / `breaker.timeout` are unparseable

The loader normalizes:
- `strategy` empty → `weighted_random`
- `max_attempts <= 0` → `3`
- `breaker.failure_ratio <= 0 or > 1` → `0.5`
- `breaker.min_requests == 0` → `5`
- `breaker.max_requests == 0` → `1`

The validation lives in `completion/pool/config.go:validate()`.

---

## 11. Worked examples

### 11.1 Two providers, 75% primary / 25% fallback, no breaker

```json
{
  "strategy": "weighted_random",
  "max_attempts": 2,
  "endpoints": [
    { "name": "primary", "url": "https://api.openai.com/v1/chat/completions",
      "api_key_env": "OPENAI_KEY_PRIMARY", "weight": 3, "enabled": true },
    { "name": "fallback", "url": "https://x.openai.azure.com/.../chat/completions?api-version=2024-02-15-preview",
      "api_key_env": "AZURE_KEY_FALLBACK", "weight": 1, "enabled": true }
  ]
}
```

### 11.2 Model-specific routing

GPT-4o requests go to OpenAI, Claude requests go to Anthropic-compatible adapter, everything else to a generic catch-all:

```json
{
  "strategy": "weighted_random",
  "max_attempts": 2,
  "endpoints": [
    { "name": "openai-gpt4o",  "url": "https://api.openai.com/v1/chat/completions",
      "api_key_env": "OPENAI_KEY", "weight": 1, "models": ["gpt-4o","gpt-4o-mini"], "enabled": true },
    { "name": "claude-adapter","url": "https://your-adapter/v1/chat/completions",
      "api_key_env": "ANTHROPIC_KEY", "weight": 1, "models": ["claude-3-5-sonnet","claude-3-haiku"], "enabled": true },
    { "name": "catchall",      "url": "https://api.openai.com/v1/chat/completions",
      "api_key_env": "OPENAI_KEY", "weight": 1, "models": ["*"], "enabled": true }
  ]
}
```

> Caveat: if the request's `model` matches **only** the `catchall` (via `["*"]`), `weighted_random` will still pick among all candidates that pass the `model_affinity` filter — which in this case is just `catchall`. Catch-all entries do not weaken specificity for matching requests because the filter runs first.

### 11.3 Latency-aware with circuit breaking

```json
{
  "strategy": "ewma_latency",
  "max_attempts": 3,
  "breaker": {
    "enabled": true,
    "interval": "60s", "timeout": "30s",
    "failure_ratio": 0.5, "min_requests": 10, "max_requests": 1
  },
  "endpoints": [
    { "name": "us-east",   "url": "...", "api_key_env": "K1", "weight": 1, "enabled": true },
    { "name": "eu-west",   "url": "...", "api_key_env": "K2", "weight": 1, "enabled": true },
    { "name": "ap-tokyo",  "url": "...", "api_key_env": "K3", "weight": 1, "enabled": true }
  ]
}
```

Each region's latency is independently tracked; traffic gravitates to the fastest. If one region goes flaky, its breaker trips after ~10 requests at >50% failure rate, traffic instantly shifts off it, and the breaker probes every 30s with one trial request.

### 11.4 Migrating from legacy `COMPL_ENDPOINT`

Old `.env`:
```bash
COMPL_ENDPOINT=https://api.openai.com/v1/chat/completions
COMPL_API_KEY=sk-abc123
```

Minimum-change JSON equivalent (`config/pool-minimal.example.json` is essentially this):
```bash
OPENAI_KEY=sk-abc123
COMPL_POOL_CONFIG_FILE=/etc/llm_gateway/pool.json
```

```json
{
  "strategy": "weighted_random",
  "max_attempts": 1,
  "endpoints": [
    { "name": "primary",
      "url": "https://api.openai.com/v1/chat/completions",
      "api_key_env": "OPENAI_KEY",
      "weight": 1, "enabled": true }
  ]
}
```

After this works, gradually add a fallback endpoint, then bump `max_attempts` to 2 or 3, then enable the breaker.

---

## 12. Runtime mutation

Every field in `endpoints[*]` can be changed at runtime via the gateway admin API:

| Operation | Endpoint | Body |
|---|---|---|
| List | `GET /admin/completion/endpoints` | — |
| Add | `POST /admin/completion/endpoint` | EndpointSpec |
| Remove | `DELETE /admin/completion/endpoint` | `{"name":"..."}` |
| Change weight | `POST /admin/completion/endpoint/weight` | `{"name":"...", "weight": N}` |
| Enable / disable | `POST /admin/completion/endpoint/enabled` | `{"name":"...", "enabled": true|false}` |
| Reset breaker | `POST /admin/completion/breaker/reset` | `{"name":"..."}` |

Implementation details (`completion/pool/admin.go`):
- All mutations take a write lock and perform **copy-on-write** on the endpoints slice. `Stats` and `Breaker` pointers are preserved across the rebuild, so counters and circuit state survive a `Reweight` / `SetEnabled` operation.
- In-flight requests hold a snapshot taken at the top of `GetStream`; admin changes do not affect their behavior. They complete normally.
- `AddEndpoint` runs the same validation as the startup loader.
- `ResetBreaker` errors with `424 Failed Dependency` if the pool was started with `breaker.enabled: false` — there's nothing to reset.

See [`docs/api.md` § 3.3](api.md#33-completion-上游池管理) for full HTTP request/response shapes.

---

## 13. Multi-replica semantics

The pool is **in-process state**. When `completion-service` runs with N replicas behind etcd discovery:
- Every replica reads the same `COMPL_POOL_CONFIG_FILE` at startup → same initial pool
- Every replica maintains its **own** breaker counters, EWMA latencies, in-flight counts
- Admin mutations land on **one** replica (whichever the gateway's gRPC round-robin picked)

What this means in practice:
- Stats reported via `/admin/completion/stats` reflect one replica only. To see the cluster, aggregate across replicas externally.
- A `Reweight` call only affects the receiving replica. To roll out a weight change globally, either: (a) call the admin endpoint N times (gateway round-robin may or may not hit every replica — not deterministic), or (b) update the config file and `docker compose restart completion-service` for an authoritative reload.
- Breaker state is local. One replica's `open` breaker on `endpoint-a` does **not** prevent another replica from trying `endpoint-a`. This is usually fine — correlated failures will trip every replica's breaker independently within seconds.

A future enhancement could broadcast mutations via etcd watch, but that's out of scope for the current implementation. The trade-off was: keep the pool simple and local; treat the config file as the authoritative cluster-wide state; use admin RPCs for tactical, single-replica probes/overrides.

---

## 14. FAQ / pitfalls

**Q: I get `pool: endpoint "x" url invalid: ...` at startup.**
The URL string couldn't be parsed by `net/url.Parse`. Check for unescaped characters, missing `https://`, or stray whitespace. Azure URLs need the `?api-version=...` query string but the rest of the path must be exact.

**Q: I add a new endpoint via admin, but it never gets picked.**
Most likely cause: the `models` list doesn't match the requests' `model` field. Run `GET /admin/completion/endpoints` and confirm. Also check the request isn't being filtered out by the breaker (`breaker_state`).

**Q: My breaker keeps tripping after 1 failure even with `min_requests: 5`.**
gobreaker's `min_requests` is evaluated *within the current interval*. If your traffic is sparse, the interval may roll over before 5 requests accumulate. Increase `interval` (e.g. `"5m"`) or reduce `min_requests`.

**Q: Two endpoints share the same key — do I need two `api_key_env`?**
No. Both endpoints can reference the same env var name; the env var only needs to be set once in the process. The duplicate `api_key_env` value in the config is fine.

**Q: How do I roll out a config change without a deploy?**
Edit the JSON file → run a fleet-wide `docker compose restart completion-service`. The restart is fast (sub-second per replica) and graceful (in-flight requests drain). Admin API mutations are tactical, not for permanent state changes.

**Q: I disabled an endpoint via admin; will it come back on restart?**
Yes. Restart re-reads `COMPL_POOL_CONFIG_FILE`, which is the source of truth. Admin mutations are in-memory only.

**Q: Can I run the gateway without any pool config?**
No — `completion-service` must have at least one of the three sources set, or it exits at startup. The legacy `COMPL_ENDPOINT` mode is the simplest fallback.

---

## 15. Source pointers

| Concern | File |
|---|---|
| Loader, env precedence, validation | `completion/pool/config.go` |
| Pool service, retry loop, channel wrap | `completion/pool/pool.go` |
| Endpoint type, internal client interface | `completion/pool/endpoint.go` |
| `weighted_random` selector | `completion/pool/selector.go` |
| `least_pending` selector | `completion/pool/selector_lp.go` |
| `ewma_latency` selector | `completion/pool/selector_ewma.go` |
| Filters (model affinity, breaker open) | `completion/pool/filter.go` |
| Breaker config & factory | `completion/pool/breaker.go` |
| Stats counters & EWMA | `completion/pool/stats.go` |
| Runtime mutation (admin) | `completion/pool/admin.go` |
| gRPC server / client (Service, StatsProvider, Admin) | `completion/grpc/server.go`, `completion/grpc/admin_server.go`, `completion/grpc/client.go` |
| Gateway admin HTTP handlers | `gateway/admin_pool.go` |
