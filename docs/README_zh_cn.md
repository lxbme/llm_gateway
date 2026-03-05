# LLM Gateway

一个轻量级、兼容 OpenAI 的 API 网关，具有语义缓存和基于令牌的身份验证功能。可以作为 OpenAI API 端点的直接替代，只需将现有客户端指向此网关即可。

## 功能

- **兼容 OpenAI API** — 支持任何兼容 OpenAI Chat Completions API (`/v1/chat/completions`) 的客户端
- **语义缓存** — 使用向量相似性搜索（Qdrant）为语义等效的提示提供缓存响应，大幅减少上游 API 调用和延迟
- **基于令牌的身份验证** — `sk-xxx` 风格的 API 密钥存储在 Redis 中，在任何网络查询之前进行格式验证（CRC32 校验）
- **流式支持** — 完整的 SSE 流式传输透传和缓存响应流式支持
- **微服务架构** — 每个功能（嵌入、缓存、完成、认证）都是一个独立的 gRPC 服务，可独立部署和扩展
- **CORS 支持** — 内置 CORS 中间件，支持基于浏览器的客户端

## 架构

![Architecture-light](./docs/llm_gateway_struct_light.png#gh-light-mode-only)

![Architecture-dark](./docs/llm_gateway_struct_dark.png#gh-dark-mode-only)

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
podman-compose up --build
# 或者
docker compose up --build
```

### 3. 创建您的第一个 API 令牌

```sh
curl -s -X POST http://localhost:8081/admin/create \
     -H "Content-Type: application/json" \
     -d '{"alias": "my-user"}'
# 响应: {"token":"sk_xxx","alias":"my-user"}
```

### 4. 发起请求

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

## 示例 `.env`

```env
# 嵌入服务
EMBED_API_KEY=sk-your-embedding-api-key
EMBED_ENDPOINT=https://api.openai.com/v1/embeddings

# 完成服务
COMPL_API_KEY=sk-your-completion-api-key
COMPL_ENDPOINT=https://api.openai.com/v1/chat/completions

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
|----------|---------|-------------|
| `CACHE_ADDR` | `localhost:50052` | 缓存服务 gRPC 地址 |
| `COMPL_ADDR` | `localhost:50053` | 完成服务 gRPC 地址 |
| `AUTH_ADDR` | `localhost:50054` | 认证服务 gRPC 地址 |
| `LOG_LEVEL` | `ERROR` | 日志详细程度: `DEBUG`, `INFO`, `ERROR` |
| `DEBUG_MODE` | `false` | 设置为 `true` 启用 `/debug/pprof/*` 端点 |

### 嵌入服务 (`embedding-service`)

| 变量 | 默认值 | 描述 |
|----------|---------|-------------|
| `EMBED_API_KEY` | — | 嵌入提供商的 API 密钥 |
| `EMBED_ENDPOINT` | — | **必填.** 嵌入 API 端点 URL |
| `SERVE_PORT` | `50051` | gRPC 监听端口 |

### 缓存服务 (`cache-service`)

| 变量 | 默认值 | 描述 |
|----------|---------|-------------|
| `EMBED_ADDR` | `localhost:50051` | 嵌入服务 gRPC 地址 |
| `QDRANT_HOST` | — | Qdrant 主机名 |
| `QDRANT_PORT` | — | Qdrant gRPC 端口 |
| `SERVE_PORT` | `50052` | gRPC 监听端口 |

### 完成服务 (`completion-service`)

| 变量 | 默认值 | 描述 |
|----------|---------|-------------|
| `COMPL_API_KEY` | — | 完成提供商的 API 密钥 |
| `COMPL_ENDPOINT` | — | **必填.** 聊天完成 API 端点 URL |
| `SERVE_PORT` | `50053` | gRPC 监听端口 |

### 认证服务 (`auth-service`)

| 变量 | 默认值 | 描述 |
|----------|---------|-------------|
| `REDIS_ADDR` | — | **必填.** Redis 地址 (`host:port`) |
| `REDIS_PASSWORD` | `""` | Redis 密码（如果没有则留空） |
| `REDIS_DB` | — | **必填.** Redis 数据库索引 |
| `SERVE_PORT` | `50054` | gRPC 监听端口 |

***REDIS_ADDR** 默认指向 docker compose 文件中的 redis docker.*

## 管理 API

管理 API 监听 `:8081`（仅绑定到 `127.0.0.1`）。

**需要认证.** 每个请求必须包含与网关上设置的 `ADMIN_SECRET` 环境变量匹配的 `X-Admin-Secret` 头。如果 `ADMIN_SECRET` 为空或头缺失/错误，请求将被拒绝并返回 `403 Forbidden` — 在未配置密钥之前，所有管理端点都将被有效禁用。

```sh
curl -s -X POST http://localhost:8081/admin/create \
     -H "X-Admin-Secret: your-secret" \
     -H "Content-Type: application/json" \
     -d '{"alias": "alice"}'
```

| 方法 | 路径 | 请求体 | 描述 |
|--------|------|------|-------------|
| `POST` | `/admin/create` | `{"alias": "name"}` | 生成一个新的 `sk_xxx` 令牌 |
| `POST` | `/admin/get` | `{"token": "sk_xxx"}` | 查询令牌的有效性和别名 |
| `POST` | `/admin/delete` | `{"token": "sk_xxx"}` | 撤销令牌 |

### 网关管理配置

| 变量 | 默认值 | 描述 |
|----------|---------|-------------|
| `ADMIN_SECRET` | — | **必填.** `X-Admin-Secret` 头的共享密钥。未配置密钥时管理 API 将被禁用。 |

## 许可证

查看 [LICENSE](LICENSE)。
