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
cp config/.env.example .env
# 编辑 .env 文件，填写您的 API 密钥和端点。更多示例（多端点池、横向扩容）
# 见 config/ 目录，所有变量的语义见根 README 的 Configuration Reference。
```

### 2. 启动所有服务

仓库提供了一个 helper 脚本，会根据 `.env`（或当前 shell 环境）里的 provider
自动拼上正确的 Compose overlay：

```sh
# dev 模式（本地构建，使用 docker-compose.yml）
bash docker-run.sh -d

# prod 模式（拉取预构建的 ghcr.io 镜像，使用 docker-compose.prod.yml）
bash docker-run.sh --prod -d
```

也可以直接调用 `docker compose`。不带 overlay 的最小调用：

```sh
docker compose -f docker-compose.prod.yml up -d
```

如果 `.env` 中启用了可选后端（`EMBED_PROVIDER=ollama` 或
`CACHE_STORE_PROVIDER=redis_hnsw`），需要追加对应的 overlay：

```sh
docker compose \
  -f docker-compose.prod.yml \
  -f config/docker-compose.ollama.yml \
  -f config/docker-compose.hnsw.yml \
  up -d
```

关停服务（helper 会自动带上当前 provider 对应的 overlay 集合）：

```sh
bash docker-run.sh --prod --down
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

所有 provider 通用的变量：

| 变量 | 默认值 | 描述 |
|------|--------|------|
| `EMBED_PROVIDER` | — | **必填。** Embedding provider 名称：`openai` 或 `ollama` |
| `EMBED_ENDPOINT` | — | **必填。** 嵌入 API 端点 URL（不同 provider 的路径不同） |
| `EMBED_MODEL` | — | **必填。** Embedding 模型名称 |
| `EMBED_DIMENSIONS` | — | **必填。** 向量维度；必须与模型真实输出一致 |
| `SERVE_PORT` | `50051` | gRPC 监听端口 |

`embedding-service` 通过 `EMBED_PROVIDER` 选择 provider。已实现的 provider：`openai`、`ollama`。下面各小节列出每个 provider 专属的变量 —— 只有匹配所选 provider 的变量才会被读取。

#### `openai` provider（OpenAI 兼容 HTTP API）

设置 `EMBED_PROVIDER=openai` 即可调用任何 OpenAI 兼容的 `/v1/embeddings` 端点。

| 变量 | 默认值 | 描述 |
|------|--------|------|
| `EMBED_API_KEY` | — | **必填。** 写入 `Authorization` 头的 Bearer token |

请求体包含 `{model, input, encoding_format, dimensions}`，从响应的 `data[].embedding` 解析向量。

#### `ollama` provider（Ollama 本地模型）

设置 `EMBED_PROVIDER=ollama` 调用本地或自托管的 [Ollama](https://ollama.com/) daemon（`POST /api/embed`）。

| 变量 | 默认值 | 描述 |
|------|--------|------|
| `EMBED_API_KEY` | — | *可选。* 默认 ollama 无需鉴权，留空即可；前置鉴权反向代理时再填 |

请求体只含 `{model, input}`，从响应的 `embeddings[0]` 解析向量。启动时会发起一次 probe 请求，**若模型实际维度与 `EMBED_DIMENSIONS` 不一致则立刻 fail**。

示例：

```bash
EMBED_PROVIDER=ollama
EMBED_ENDPOINT=http://localhost:11434/api/embed
EMBED_MODEL=qwen3-embedding:0.6b
EMBED_DIMENSIONS=1024   # 必须与模型实际输出维度一致
EMBED_API_KEY=          # 默认 ollama 留空
```

若要让容器内的 `embedding-service` 访问宿主机的 ollama，使用项目自带的 overlay 解析 `host.docker.internal`：

```bash
docker compose \
  -f docker-compose.yml \
  -f config/docker-compose.ollama.yml \
  up -d --build embedding-service
```

### 缓存服务 (`cache-service`)

所有后端通用的变量：

| 变量 | 默认值 | 描述 |
|------|--------|------|
| `CACHE_MODE` | `semantic` | 缓存模式。`semantic` 需要 embedding 服务；`exact` 预留给未来的精确匹配存储 |
| `CACHE_STORE_PROVIDER` | — | **必填。** 底层 store provider 名称：`qdrant` 或 `redis_hnsw` |
| `CACHE_PROVIDER` | — | `CACHE_STORE_PROVIDER` 的兼容别名 |
| `CACHE_BUFFER_SIZE` | `1000` | 异步缓存写入队列容量 |
| `CACHE_WORKER_COUNT` | `5` | 异步缓存 worker 数量 |
| `EMBED_ADDR` | `localhost:50051` | 嵌入服务 gRPC 地址 |
| `SERVE_PORT` | `50052` | gRPC 监听端口 |

`cache-service` 通过 `CACHE_MODE + CACHE_STORE_PROVIDER` 选择底层实现。已实现的组合：`semantic + qdrant` 与 `semantic + redis_hnsw`。当 `CACHE_MODE=semantic` 时，`EMBED_ADDR` 必须指向可用的 embedding 服务，缓存服务会先生成向量，再执行查询或写入。每个后端独有的变量见下方对应小节，缓存服务只读取所选 provider 对应的那一组变量。

#### `qdrant` provider（Qdrant 向量数据库）

设置 `CACHE_STORE_PROVIDER=qdrant` 即可使用独立的 Qdrant 实例作为语义缓存后端（默认 `docker-compose.yml` 已经预配置）。

| 变量 | 默认值 | 描述 |
|------|--------|------|
| `QDRANT_HOST` | `localhost` | Qdrant 主机名 |
| `QDRANT_PORT` | `6334` | Qdrant gRPC 端口 |
| `QDRANT_COLLECTION_NAME` | `llm_semantic_cache` | 语义缓存使用的 Qdrant collection 名称 |
| `QDRANT_SIMILARITY_THRESHOLD` | `0.95` | 判定缓存命中的最低余弦相似度阈值 |

#### `redis_hnsw` provider（Redis Stack + RediSearch HNSW）

设置 `CACHE_STORE_PROVIDER=redis_hnsw` 即可改用 Redis Stack 作为语义缓存后端，不再依赖 Qdrant。要求 Redis 实例已加载 RediSearch 模块（如 `redis/redis-stack-server:latest`）。

容器部署时叠加专用 overlay，**主 `docker-compose.yml` 保持不变**：

```bash
CACHE_STORE_PROVIDER=redis_hnsw docker compose \
  -f docker-compose.yml -f config/docker-compose.hnsw.yml \
  up -d --build
```

| 变量 | 默认值 | 描述 |
|------|--------|------|
| `REDIS_HNSW_ADDR` | `localhost:6379` | Redis Stack 主机:端口 |
| `REDIS_HNSW_PASSWORD` | _(空)_ | AUTH 密码（如启用） |
| `REDIS_HNSW_DB` | `0` | 逻辑 DB 编号 |
| `REDIS_HNSW_INDEX_NAME` | `llm_semantic_cache_idx` | RediSearch 索引名 |
| `REDIS_HNSW_KEY_PREFIX` | `llm_semantic_cache` | Hash 键前缀（索引 `PREFIX` 为 `<prefix>:`） |
| `REDIS_HNSW_SIMILARITY_THRESHOLD` | `0.95` | 命中阈值（cosine 路径：`1 - distance >= threshold`） |
| `REDIS_HNSW_DISTANCE_METRIC` | `COSINE` | `COSINE` / `L2` / `IP`。非 cosine 时需重新校准阈值 |
| `REDIS_HNSW_M` | `16` | HNSW 图度数 |
| `REDIS_HNSW_EF_CONSTRUCTION` | `200` | 构图候选数 |
| `REDIS_HNSW_EF_RUNTIME` | `10` | 查询候选数；调大可换更高召回 |
| `REDIS_HNSW_RECORD_TTL_SECONDS` | `0` | 单条记录 TTL（秒），`0` 表示不过期 |
| `REDIS_HNSW_DIAL_TIMEOUT_MS` | `5000` | Redis 客户端连接超时 |

### Completion服务 (`completion-service`)

> 📘 **详细参考：** [`docs/pool_config_zh_cn.md`](pool_config_zh_cn.md)（或 [English](pool_config.md)）覆盖了每个字段、校验规则、策略、熔断调优、运行时变更、多副本语义和迁移步骤。下面只是速览。

完成服务按下面三种来源之一读取上游池配置，优先级从高到低：

1. `COMPL_POOL_CONFIG_FILE` — JSON 文件路径（最高）
2. `COMPL_POOL_CONFIG` — 内联 JSON 字符串
3. `COMPL_ENDPOINT` + `COMPL_API_KEY` — 旧的单上游回退（最低）

三者全空 → 启动报错退出。单上游路径保留是为了向后兼容，会在启动时打 deprecation 警告。

| 变量 | 默认值 | 描述 |
|------|--------|------|
| `COMPL_POOL_CONFIG_FILE` | — | JSON 池配置文件路径。最高优先级。 |
| `COMPL_POOL_CONFIG` | — | 内联 JSON 池配置。仅在 `COMPL_POOL_CONFIG_FILE` 为空时使用。 |
| `COMPL_ENDPOINT` | — | 旧的单上游 URL。仅在两个 JSON 变量都没设时使用，内部合成一个 1-端点池。 |
| `COMPL_API_KEY` | — | 旧模式下，openai client 读这个 env 拿真实 key。 |
| *被引用的各 API key 变量* | — | JSON 模式下，每个 endpoint 的 `api_key_env` 字段指向一个 env 变量名，那个变量存的是真实 key（如 `OPENAI_KEY_PRIMARY`）。这些变量必须出现在 completion-service 进程的 env 里。 |
| `SERVE_PORT` | `50053` | gRPC 监听端口 |

JSON schema 完整字段、三种 strategy 语义、filter 链顺序、熔断参数、运行时变更接口——见 [`docs/pool_config_zh_cn.md`](pool_config_zh_cn.md)。

### RAG 服务 (`rag-service`)

| 变量 | 默认值 | 描述 |
|------|--------|------|
| `EMBED_ADDR` | `localhost:50051` | 嵌入服务 gRPC 地址 |
| `QDRANT_HOST` | `localhost` | Qdrant 主机名 |
| `QDRANT_PORT` | `6334` | Qdrant gRPC 端口 |
| `QDRANT_COLLECTION_NAME` | `llm_rag_documents` | RAG 文档使用的 Qdrant collection（与缓存 collection 独立） |
| `RAG_SIMILARITY_THRESHOLD` | `0.6` | 文档块被检索的最低余弦相似度。设为 `0` 可完全禁用相似度过滤（任何匹配 collection 的文档块都会被返回，仍受 `RAG_DEFAULT_TOP_K` 限制） |
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

> 📘 **跨主机部署？** 完整的分布式部署参考（etcd 集群、`ADVERTISE_ADDR` 规则、端口与防火墙清单、四主机完整示例）见 [分布式部署指南](distributed_deployment_zh_cn.md)（[English](distributed_deployment.md)）。

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

### 调试检索

如果发现 RAG 上下文始终没有被注入到 LLM 回复中，把网关的 `LOG_LEVEL` 设为 `DEBUG`，每个请求会打印：

- `RAG: resolved collection="<name>" from <source>` — 实际查询使用的 collection 名以及它的来源（`X-RAG-Collection header` 或 `Auth.Subject (token alias)`）。
- `RAG: retrieve returned 0 chunks for collection="<name>"` — 命中数为 0 时输出。最常见的原因是 ingest 时使用的 `collection` 名与查询时解析出的名字不一致。

当网关已配置 RAG 但无法解析任何 collection（既没有 `X-RAG-Collection` header，**且**当前 token 也没有别名）时，会输出 warning 并跳过检索。

实例：如果 ingest 时使用 `collection: "mr"`，但请求所用 token 的别名是 `test_token`，网关会去查询 collection `test_token` 而无任何命中。两种解决方式：在请求上加 `-H "X-RAG-Collection: mr"`，或用 token 别名重新 ingest 一次。

如需完全绕过分数过滤来验证问题，可以把 `rag-service` 的 `RAG_SIMILARITY_THRESHOLD` 设为 `0` 后重启 — 启动日志会显示 `threshold=0.0000`，qdrant 查询将跳过 score filter，便于确认问题是否出在 collection 过滤。

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
