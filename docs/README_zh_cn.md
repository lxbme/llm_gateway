# LLM Gateway

[English](../README.md) | [中文](README_zh_cn.md)

一个轻量级、兼容 OpenAI 的 API 网关，具有语义缓存、RAG（检索增强生成）和基于令牌的身份验证功能。可以作为 OpenAI API 端点的直接替代，只需将现有客户端指向此网关即可。

## 功能

- **兼容 OpenAI API** — 支持任何兼容 OpenAI Chat Completions API (`/v1/chat/completions`) 的客户端
- **语义缓存** — 使用向量相似性搜索（Qdrant）为语义等效的提示提供缓存响应，大幅减少上游 API 调用和延迟
- **RAG（检索增强生成）** — 可选知识库注入：通过管理 API 上传文档资料，网关自动检索相关内容并拼接到每个 prompt 之前
- **基于令牌的身份验证** — `sk-xxx` 风格的 API 密钥存储在 Redis 中，在任何网络查询之前进行格式验证（CRC32 校验）
- **流式支持** — 完整的 SSE 流式传输透传和缓存响应流式模拟
- **微服务架构** — 每个功能（嵌入、缓存、完成、认证、RAG）都是一个独立的 gRPC 服务，可独立部署和扩展
- **CORS 支持** — 内置 CORS 中间件，支持基于浏览器的客户端

## 架构

![Architecture-light](./llm_gateway_struct_light.png#gh-light-mode-only)

![Architecture-dark](./llm_gateway_struct_dark.png#gh-dark-mode-only)

## 快速开始

### 前置条件

- Docker / Podman + Compose
- 一个兼容 OpenAI 的 API 密钥和端点（用于嵌入和完成）

### 1. 克隆并配置

```sh
git clone https://github.com/lxbme/llm_gateway.git
cd llm_gateway
cp .env.example .env
# 编辑 .env 文件，填写您的 API 密钥和端点
```

### 2. 启动所有服务

```sh
podman compose -f docker-compose.prod.yml up -d
# 或者
docker compose -f docker-compose.prod.yml up -d
```

### 3. 创建您的第一个 API 令牌

```sh
bash -lc 'set -a; source test/cli/.env; set +a; curl -sS -X POST http://127.0.0.1:8081/admin/create -H "X-Admin-Secret: $ADMIN_SECRET" -H "Content-Type: application/json" -d "{\"alias\":\"manual-test\"}"'
# 响应: {"token":"sk_xxx","alias":"my-user"}
```

### 4. 发起请求

sk_xxx 是运行上一个命令得到的

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

## 示例 `.env`

```env
# 嵌入服务
EMBED_PROVIDER=openai
EMBED_API_KEY=sk-your-embedding-api-key
EMBED_ENDPOINT=https://api.openai.com/v1/embeddings
EMBED_MODEL=text-embedding-3-small
EMBED_DIMENSIONS=1536

# 缓存服务
CACHE_MODE=semantic
CACHE_STORE_PROVIDER=qdrant
CACHE_BUFFER_SIZE=1000
CACHE_WORKER_COUNT=5
QDRANT_SIMILARITY_THRESHOLD=0.95
# QDRANT_COLLECTION_NAME 在缓存服务中默认为 llm_semantic_cache

# 完成服务
COMPL_API_KEY=sk-your-completion-api-key
COMPL_ENDPOINT=https://api.openai.com/v1/chat/completions

# RAG 服务（可选 — 将 RAG_ADDR 留空可完全禁用 RAG）
# RAG_ADDR=localhost:50055
# RAG_SIMILARITY_THRESHOLD=0.6   # 默认 0.6
# RAG_DEFAULT_TOP_K=3            # 默认 3
# QDRANT_COLLECTION_NAME 在 rag 服务中默认为 llm_rag_documents

# Redis 认证数据库索引
REDIS_DB=0

# 网关日志级别: DEBUG | INFO | ERROR
LOG_LEVEL=ERROR

# 启用 pprof 性能分析端点
DEBUG_MODE=false

# 管理员 API 密钥 — 使用 /admin/* 端点所需。
# 如果未设置，所有管理请求将被拒绝并返回 403 Forbidden。
# 通过 X-Admin-Secret 请求头传递此值。
ADMIN_SECRET=change-me-to-a-strong-random-secret
```

## 配置参考

### 网关 (`gateway`)

| 变量 | 默认值 | 描述 |
|------|--------|------|
| `CACHE_ADDR` | `localhost:50052` | 缓存服务 gRPC 地址 |
| `COMPL_ADDR` | `localhost:50053` | 完成服务 gRPC 地址 |
| `AUTH_ADDR` | `localhost:50054` | 认证服务 gRPC 地址 |
| `RAG_ADDR` | `""` | RAG 服务 gRPC 地址。留空则禁用 RAG。 |
| `LOG_LEVEL` | `ERROR` | 日志详细程度：`DEBUG`、`INFO`、`ERROR` |
| `DEBUG_MODE` | `false` | 设置为 `true` 启用 `/debug/pprof/*` 端点 |

### 嵌入服务 (`embedding-service`)

| 变量 | 默认值 | 描述 |
|------|--------|------|
| `EMBED_PROVIDER` | — | **必填。** Embedding provider 名称，当前支持 `openai` |
| `EMBED_API_KEY` | — | 嵌入提供商的 API 密钥 |
| `EMBED_ENDPOINT` | — | **必填。** 嵌入 API 端点 URL |
| `EMBED_MODEL` | — | **必填。** Embedding 模型名称 |
| `EMBED_DIMENSIONS` | — | **必填。** 向量维度 |
| `SERVE_PORT` | `50051` | gRPC 监听端口 |

### 缓存服务 (`cache-service`)

| 变量 | 默认值 | 描述 |
|------|--------|------|
| `CACHE_MODE` | `semantic` | 缓存模式。`semantic` 需要 embedding 服务；`exact` 预留给未来的精确匹配存储 |
| `CACHE_STORE_PROVIDER` | — | **必填。** 底层 store provider 名称，当前支持 `qdrant` |
| `CACHE_PROVIDER` | — | `CACHE_STORE_PROVIDER` 的兼容别名 |
| `CACHE_BUFFER_SIZE` | `1000` | 异步缓存写入队列容量 |
| `CACHE_WORKER_COUNT` | `5` | 异步缓存 worker 数量 |
| `EMBED_ADDR` | `localhost:50051` | 嵌入服务 gRPC 地址 |
| `QDRANT_HOST` | `localhost` | Qdrant 主机名 |
| `QDRANT_PORT` | `6334` | Qdrant gRPC 端口 |
| `QDRANT_COLLECTION_NAME` | `llm_semantic_cache` | 语义缓存使用的 Qdrant collection 名称 |
| `QDRANT_SIMILARITY_THRESHOLD` | `0.95` | 判定缓存命中的最低余弦相似度阈值 |
| `SERVE_PORT` | `50052` | gRPC 监听端口 |

`cache-service` 通过 `CACHE_MODE + CACHE_STORE_PROVIDER` 选择底层实现。当前版本只实现了 `semantic + qdrant`。当 `CACHE_MODE=semantic` 时，`EMBED_ADDR` 必须指向可用的 embedding 服务，缓存服务会先生成向量，再执行查询或写入。

### 完成服务 (`completion-service`)

| 变量 | 默认值 | 描述 |
|------|--------|------|
| `COMPL_API_KEY` | — | 完成提供商的 API 密钥 |
| `COMPL_ENDPOINT` | — | **必填。** 聊天完成 API 端点 URL |
| `SERVE_PORT` | `50053` | gRPC 监听端口 |

### RAG 服务 (`rag-service`)

| 变量 | 默认值 | 描述 |
|------|--------|------|
| `EMBED_ADDR` | `localhost:50051` | 嵌入服务 gRPC 地址 |
| `QDRANT_HOST` | `localhost` | Qdrant 主机名 |
| `QDRANT_PORT` | `6334` | Qdrant gRPC 端口 |
| `QDRANT_COLLECTION_NAME` | `llm_rag_documents` | RAG 文档使用的 Qdrant collection（与缓存 collection 独立） |
| `RAG_SIMILARITY_THRESHOLD` | `0.6` | 文档块被检索的最低余弦相似度 |
| `RAG_DEFAULT_TOP_K` | `3` | 每次查询最多返回的文档块数 |
| `SERVE_PORT` | `50055` | gRPC 监听端口 |

### 认证服务 (`auth-service`)

| 变量 | 默认值 | 描述 |
|------|--------|------|
| `REDIS_ADDR` | — | **必填。** Redis 地址（`host:port`） |
| `REDIS_PASSWORD` | `""` | Redis 密码（如果没有则留空） |
| `REDIS_DB` | — | **必填。** Redis 数据库索引 |
| `SERVE_PORT` | `50054` | gRPC 监听端口 |

***REDIS_ADDR** 默认指向 docker compose 文件中的 redis 容器。*

## RAG（检索增强生成）

网关内置可选的 `rag-service`，可以上传知识库并让网关自动将相关文档块注入到每个 LLM prompt 中。

### 工作原理

```
用户请求
  └─ [Gateway: StageBeforeUpstream]
       ├─ auth_validate_handler   ← 验证 token，解析别名
       ├─ rag_retrieve_handler    ← 向 rag-service 查询 top-K 文档块
       │     └─ 将检索到的内容拼接到 PromptText 之前
       ├─ cache_lookup_handler    ← 基于增强后的 prompt 查询缓存
       └─ upstream_request_build_handler
```

RAG handler 是**软依赖**：如果未设置 `RAG_ADDR`，或者检索无结果，请求将正常进行，不做任何修改。

### 知识库隔离

文档块存储在单个 Qdrant collection（`llm_rag_documents`）中，通过 `collection` payload 字段进行过滤，实现多租户隔离。每个请求通过以下方式选择知识库：

1. **`X-RAG-Collection` 请求头** — 显式指定 collection 名称（优先级最高）
2. **Token 别名** — 回退到已认证 token 的别名（每用户独立知识库）

### 启动 RAG 服务

```sh
# 环境变量
EMBED_ADDR=localhost:50051
QDRANT_HOST=localhost
QDRANT_PORT=6334
QDRANT_COLLECTION_NAME=llm_rag_documents
RAG_SIMILARITY_THRESHOLD=0.6
RAG_DEFAULT_TOP_K=3
SERVE_PORT=50055

go run ./cmd/rag
```

然后告知网关 RAG 服务地址：

```sh
RAG_ADDR=localhost:50055 go run ./cmd/gateway
```

### 上传文档

**方式 A — 原始文本（推荐）：** 直接 POST 纯文本或 Markdown，网关在后台自动分段并立即返回。

```sh
curl -X POST http://localhost:8081/admin/rag/ingest/text \
     -H "X-Admin-Secret: your-secret" \
     -H "Content-Type: application/json" \
     -d '{
       "collection":    "alice",
       "source":        "docs/product-faq.md",
       "text":          "# 退款政策\n\n我们提供 30 天退款保障...\n\n如需退款，请发送邮件至 support@example.com。\n\n退款将在 5 个工作日内处理完成。",
       "chunk_size":    500,
       "chunk_overlap": 50
     }'
# 响应（202 Accepted，立即返回）：
# {"job_id":"<uuid>","collection":"alice","status":"queued","chunk_count":3}
```

`chunk_size`（默认 500）和 `chunk_overlap`（默认 50）为可选参数，单位为字符数（rune）。分段算法兼容 Markdown：标题行始终独立成块，段落按 `chunk_size` 合并，优先在句子边界处截断。实际的嵌入计算和向量存储在后台异步完成。

**方式 B — 手动分段（高级）：** 自行分段后提交。请求同步执行，所有分段写入完成后才返回。

```sh
curl -X POST http://localhost:8081/admin/rag/ingest \
     -H "X-Admin-Secret: your-secret" \
     -H "Content-Type: application/json" \
     -d '{
       "collection": "alice",
       "source":     "docs/product-faq.md",
       "chunks": [
         {"content": "我们提供 30 天退款保障...", "chunk_index": 0, "total_chunks": 3},
         {"content": "如需退款，请发送邮件至...", "chunk_index": 1, "total_chunks": 3},
         {"content": "退款将在 5 个工作日内处理...", "chunk_index": 2, "total_chunks": 3}
       ]
     }'
# 响应: {"doc_id":"<uuid>","ingested_count":3}
```

### 发起 RAG 请求

```sh
curl -s http://localhost:8080/v1/chat/completions \
     -H "Authorization: Bearer sk_xxx" \
     -H "X-RAG-Collection: alice" \
     -H "Content-Type: application/json" \
     -d '{
       "model": "gpt-4o-mini",
       "stream": true,
       "messages": [{"role": "user", "content": "你们的退款政策是什么？"}]
     }'
```

网关从 `alice` collection 中检索最相关的 top-3 文档块，并在调用 LLM 之前将其拼接到 prompt 之前。

### 删除文档

```sh
curl -X DELETE http://localhost:8081/admin/rag/doc \
     -H "X-Admin-Secret: your-secret" \
     -H "Content-Type: application/json" \
     -d '{"doc_id": "<uuid>", "collection": "alice"}'
# 响应: 204 No Content
```

---

## 管理 API

管理 API 监听 `:8081`（仅绑定到 `127.0.0.1`）。

**需要认证。** 每个请求必须包含与网关上设置的 `ADMIN_SECRET` 环境变量匹配的 `X-Admin-Secret` 头。如果 `ADMIN_SECRET` 为空或头缺失/错误，请求将被拒绝并返回 `403 Forbidden` — 在未配置密钥之前，所有管理端点都将被禁用。

```sh
curl -s -X POST http://localhost:8081/admin/create \
     -H "X-Admin-Secret: your-secret" \
     -H "Content-Type: application/json" \
     -d '{"alias": "alice"}'
```

**令牌管理**

| 方法 | 路径 | 请求体 | 描述 |
|------|------|--------|------|
| `POST` | `/admin/create` | `{"alias": "name"}` | 生成一个新的 `sk_xxx` 令牌 |
| `POST` | `/admin/get` | `{"token": "sk_xxx"}` | 查询令牌的有效性和别名 |
| `POST` | `/admin/delete` | `{"token": "sk_xxx"}` | 撤销令牌 |

**RAG 知识库管理**

| 方法 | 路径 | 请求体 | 描述 |
|------|------|--------|------|
| `POST` | `/admin/rag/ingest/text` | `{"collection","source","text","chunk_size","chunk_overlap"}` | 上传原始文本/Markdown，**自动分段**，**异步**（202） |
| `POST` | `/admin/rag/ingest` | `{"collection","source","chunks":[...]}` | 上传已分段内容，**同步** |
| `DELETE` | `/admin/rag/doc` | `{"doc_id","collection"}` | 删除文档的所有分段 |

### 网关管理配置

| 变量 | 默认值 | 描述 |
|------|--------|------|
| `ADMIN_SECRET` | — | **必填。** `X-Admin-Secret` 头的共享密钥。未配置时管理 API 将被禁用。 |

## 许可证

查看 [LICENSE](../LICENSE)。
