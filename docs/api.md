# LLM Gateway HTTP API

本文整理 `llm_gateway` 对外暴露的全部 HTTP 接口，供前端 / 运维工具集成使用。

## 1. 概览

| 端口 | 用途 | 认证 |
|---|---|---|
| `8080` | 普通用户 API（OpenAI 兼容） | `Authorization: Bearer <token>` |
| `8081` | Admin API（token 管理 / RAG 管理 / 上游池管理） | `X-Admin-Secret: <ADMIN_SECRET>` |

- Admin 端口仅监听 `127.0.0.1`，生产部署中不应直接暴露到公网。
- Public 端口流式响应使用 **Server-Sent Events**（`Content-Type: text/event-stream`），与 OpenAI Chat Completions 流式协议一致。
- 所有 JSON 响应使用 UTF-8。

---

## 2. 普通用户 API（:8080）

### 2.1 `POST /v1/chat/completions`

OpenAI 兼容的对话补全接口。**始终以 SSE 流式响应**，无论请求中 `stream` 字段为何值。

#### 请求头

| 头 | 必填 | 说明 |
|---|---|---|
| `Authorization` | ✅ | `Bearer <token>`。token 由 admin API 创建（见 §3.1）。 |
| `Content-Type` | ✅ | `application/json` |
| `X-RAG-Collection` | ❌ | 若指定，触发 RAG 检索并将命中的上下文拼到 prompt 前；不指定时会 fallback 到 token 对应的 alias 作为 collection 名 |
| `x-mock` | ❌ | 设为 `true` 时不调用真实上游，返回 mock 流；用于联调 |

#### 请求体

```json
{
  "model": "gpt-4o-mini",
  "stream": true,
  "temperature": 0.7,
  "max_tokens": 512,
  "messages": [
    { "role": "system", "content": "You are a helpful assistant." },
    { "role": "user",   "content": "Hello, who are you?" }
  ]
}
```

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `model` | string | ✅ | 上游 LLM 模型名。若 completion-service 配置了 `model_affinity`，决定路由到哪个上游 endpoint |
| `messages` | array | ✅ | 标准 OpenAI 消息数组，`role ∈ {system, user, assistant}` |
| `stream` | bool | ❌ | 兼容字段；网关总是流式回写 |
| `temperature` | float | ❌ | 透传给上游 |
| `max_tokens` | int | ❌ | 透传给上游 |

#### 响应

`200 OK` + SSE 流。每个 `data:` 行是一个 JSON 增量块；最后两行固定为完成标记 + `[DONE]`：

```
data: {"choices":[{"delta":{"content":"Hello"},"finish_reason":""}]}

data: {"choices":[{"delta":{"content":"!"},"finish_reason":""}]}

data: {"id":"chatcmpl-...", "object":"chat.completion.chunk", "created":1700000000, "model":"gpt-4o-mini", "choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
```

错误响应（非 200）使用普通 JSON：

```json
{ "error": "<message>" }
```

| 状态码 | 触发场景 |
|---|---|
| `400` | 请求体不是合法 JSON / 缺字段 |
| `401` | 缺 `Authorization` 头、token 格式错、token 失效 |
| `429` | 全局速率限制触发（`type: server_busy`） |
| `502` | 上游池整体不可达（所有端点都失败 / 熔断） |
| `500` | 内部错误 |

#### 示例

```bash
curl -N -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [{"role":"user","content":"Hi"}]
  }'
```

---

## 3. Admin API（:8081）

所有 admin 路由都必须带 `X-Admin-Secret: $ADMIN_SECRET` 头。缺失或不匹配返回 `401`。

错误响应统一为：
```json
{ "error": "<message>" }
```

成功响应：mutation 类返回 `{"ok": true}`，查询类返回业务数据。

### 3.1 Token 管理

#### `POST /admin/create` — 创建 token

```json
// request
{ "alias": "team-a" }

// response 200
{ "token": "lkg_xxxxxxxxxxxx", "alias": "team-a" }
```

`alias` 一般是团队名 / 用户名；既用于 token 元数据，也是 RAG 默认 collection。

#### `POST /admin/get` — 查询 token

```json
// request
{ "token": "lkg_..." }

// response 200
{ "valide": true, "token": "lkg_...", "alias": "team-a" }
```

> 注意：响应字段名是 `valide`（历史拼写遗留），不是 `valid`。

#### `POST /admin/delete` — 删除 token

```json
// request
{ "token": "lkg_..." }

// response 200
{ "token": "lkg_...", "status": "deleted" }
```

### 3.2 RAG 管理

仅当 gateway 启用 RAG（设置了 `RAG_ADDR`）时可用；未启用时返回 `503`。

#### `POST /admin/rag/ingest` — 写入预切片的文档

调用方负责切片。适合从外部分词流水线导入。

```json
// request
{
  "collection": "team-a",
  "source": "docs/faq.md",
  "chunks": [
    { "content": "...chunk text 1...", "chunk_index": 0, "total_chunks": 3 },
    { "content": "...chunk text 2...", "chunk_index": 1, "total_chunks": 3 },
    { "content": "...chunk text 3...", "chunk_index": 2, "total_chunks": 3 }
  ]
}

// response 200
{ "doc_id": "f3a9...uuid", "ingested_count": 3 }
```

#### `POST /admin/rag/ingest/text` — 服务端自动切片

接收纯文本/Markdown，服务端切块并**异步**入库。立即返回 `job_id`。

```json
// request
{
  "collection":    "team-a",
  "source":        "docs/faq.md",
  "text":          "...plain text or markdown...",
  "chunk_size":    500,
  "chunk_overlap": 50
}

// response 202
{
  "job_id": "uuid",
  "collection": "team-a",
  "status": "queued",
  "chunk_count": 12
}
```

`chunk_size` 默认 500，`chunk_overlap` 默认 50。

#### `DELETE /admin/rag/doc` — 删除一篇文档全部 chunk

```json
// request
{ "doc_id": "uuid", "collection": "team-a" }

// response 204 (no content)
```

### 3.3 Completion 上游池管理

直接连通到 `completion-service` 内的多上游池。运行时改动**只影响接到 RPC 的那一个 replica**——重启或多副本部署见下文「多副本注意事项」。

未启用 `CompletionAdmin` 依赖时（一般是连接 completion-service 失败），所有 `/admin/completion/*` 路由返回 `503`。

#### `GET /admin/completion/stats` — 运行时统计

```json
// response 200
{
  "endpoints": [
    {
      "endpoint": "openai-primary",
      "weight": 3,
      "enabled": true,
      "in_flight": 2,
      "success": 1450,
      "failure": 7,
      "success_rate": 0.995,
      "latency_ms_ewma": 312.45,
      "breaker_state": "closed"
    },
    {
      "endpoint": "azure-fallback",
      "weight": 1,
      "enabled": true,
      "in_flight": 0,
      "success": 12,
      "failure": 0,
      "success_rate": 1.0,
      "latency_ms_ewma": 287.10,
      "breaker_state": "closed"
    }
  ]
}
```

`breaker_state ∈ {"closed", "half_open", "open", "disabled"}`，`disabled` 表示未配置熔断。

#### `GET /admin/completion/endpoints` — 列出池成员

```json
// response 200
{
  "endpoints": [
    {
      "name": "openai-primary",
      "url": "https://api.openai.com/v1/chat/completions",
      "api_key_env": "OPENAI_KEY_PRIMARY",
      "weight": 3,
      "models": ["gpt-4o", "gpt-4o-mini"],
      "enabled": true,
      "breaker_state": "closed"
    }
  ]
}
```

#### `POST /admin/completion/endpoint` — 新增端点

```json
// request
{
  "name": "azure-secondary",
  "url": "https://x.openai.azure.com/.../chat/completions",
  "api_key_env": "AZURE_KEY_SECONDARY",
  "weight": 1,
  "models": ["*"],
  "enabled": true
}

// response 200
{ "ok": true }
```

`api_key_env` 是**环境变量名**（不是 key 本身）；completion-service 进程在调用上游时会 `os.Getenv(api_key_env)` 读取实际 key——所以新增端点前需要先把对应 env 注入到 completion-service。

`models` 为 `["*"]` 或空数组时表示接受任意模型。否则精确匹配请求里的 `model` 字段。

错误：`400`（校验失败、重名）

#### `DELETE /admin/completion/endpoint` — 移除端点

```json
// request
{ "name": "azure-secondary" }

// response 200
{ "ok": true }
```

错误：`404`（端点不存在）。**在飞请求不会被打断**——已经发出的流继续读到结束，新请求不再路由到该端点。

#### `POST /admin/completion/endpoint/weight` — 调权

```json
// request
{ "name": "openai-primary", "weight": 9 }

// response 200
{ "ok": true }
```

权重必须 `> 0`；变更立即生效。

#### `POST /admin/completion/endpoint/enabled` — 启用 / 禁用

```json
// request
{ "name": "azure-fallback", "enabled": false }

// response 200
{ "ok": true }
```

`enabled: false` 后该端点会被选择器跳过；保留 `Stats` 与 `Breaker` 状态，重新 `enabled: true` 时立即继续从中点接客。运维上常用于「快速排除某个上游做调试」。

#### `POST /admin/completion/breaker/reset` — 重置熔断器

```json
// request
{ "name": "azure-fallback" }

// response 200
{ "ok": true }
```

把对应端点的 gobreaker 计数器清零、状态切回 `closed`。仅在 pool 配置里 `breaker.enabled: true` 时有效，否则 `424 Failed Dependency`。

错误：`404`（端点不存在）/ `424`（熔断未启用）。

### 3.4 多副本注意事项

`completion-service` 可以横向扩容（etcd 服务发现 + 客户端负载均衡）。**admin 改动仅影响接到 RPC 的那一个副本**：

- 如果只有一份 replica：现状立刻生效。
- 如果有 N 份 replica：调用一次 admin 改动只影响一份；其余 N-1 份保持原状。要全网生效，目前必须**重启所有副本**（让它们从 `COMPL_POOL_CONFIG_FILE` 重新加载权威配置），或者依次直连每个副本的 admin 接口逐一改动。

未来计划：把 admin mutation 写到 etcd 触发广播。当前 phase 不实现。

---

## 4. 调试 / 观测端点

仅在启动 `cmd/gateway` 时设置环境变量 `DEBUG_MODE=true` 后挂载在 `:8080`（与公网同口！生产**勿开**）：

- `/debug/pprof/`
- `/debug/pprof/cmdline`
- `/debug/pprof/profile`
- `/debug/pprof/symbol`
- `/debug/pprof/trace`

均为 Go 标准 `net/http/pprof` 行为。

---

## 5. 常见对接代码片段

### 5.1 前端 SSE 消费（浏览器 fetch）

```js
const res = await fetch('/v1/chat/completions', {
  method: 'POST',
  headers: {
    'Authorization': `Bearer ${token}`,
    'Content-Type': 'application/json',
  },
  body: JSON.stringify({
    model: 'gpt-4o-mini',
    messages: [{ role: 'user', content: input }],
  }),
});

const reader = res.body.getReader();
const decoder = new TextDecoder();
let buf = '';
while (true) {
  const { value, done } = await reader.read();
  if (done) break;
  buf += decoder.decode(value, { stream: true });
  let nl;
  while ((nl = buf.indexOf('\n\n')) !== -1) {
    const event = buf.slice(0, nl);
    buf = buf.slice(nl + 2);
    if (!event.startsWith('data: ')) continue;
    const payload = event.slice(6);
    if (payload === '[DONE]') return;
    const obj = JSON.parse(payload);
    const delta = obj.choices?.[0]?.delta?.content || '';
    if (delta) onChunk(delta);
  }
}
```

### 5.2 Admin 后台获取池状态（浏览器）

注意 admin 端口默认只绑定 `127.0.0.1`，前端调用一般通过同主机的反向代理或运维专用网关代理过来。

```js
const res = await fetch('/admin/completion/stats', {
  headers: { 'X-Admin-Secret': adminSecret },
});
const { endpoints } = await res.json();
// render table from endpoints[]
```

### 5.3 Admin 改权重示例

```bash
curl -X POST http://127.0.0.1:8081/admin/completion/endpoint/weight \
  -H "X-Admin-Secret: $ADMIN_SECRET" \
  -H "Content-Type: application/json" \
  -d '{"name":"openai-primary","weight":10}'
```

---

## 6. 速查路由表

| 方法 | 路径 | 端口 | 用途 |
|---|---|---|---|
| POST | `/v1/chat/completions` | 8080 | 流式对话补全 |
| POST | `/admin/create` | 8081 | 创建 token |
| POST | `/admin/get` | 8081 | 查询 token |
| POST | `/admin/delete` | 8081 | 删除 token |
| POST | `/admin/rag/ingest` | 8081 | 预切片导入 RAG |
| POST | `/admin/rag/ingest/text` | 8081 | 服务端切片 + 异步入库 |
| DELETE | `/admin/rag/doc` | 8081 | 删除 RAG 文档 |
| GET | `/admin/completion/stats` | 8081 | 上游池运行时指标 |
| GET | `/admin/completion/endpoints` | 8081 | 列出上游端点 |
| POST | `/admin/completion/endpoint` | 8081 | 新增上游端点 |
| DELETE | `/admin/completion/endpoint` | 8081 | 移除上游端点 |
| POST | `/admin/completion/endpoint/weight` | 8081 | 改权 |
| POST | `/admin/completion/endpoint/enabled` | 8081 | 启用 / 禁用 |
| POST | `/admin/completion/breaker/reset` | 8081 | 重置熔断器 |
