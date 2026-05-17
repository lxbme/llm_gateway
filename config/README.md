# `config/` — Example Configurations

复制即用的配置模板。**所有文件都是示例**，不会被运行时直接加载——请按需 `cp` 出去再填值。

## 目录索引

| 文件 | 用途 | 适用场景 |
|---|---|---|
| [`.env.example`](.env.example) | 项目根 `.env` 文件模板，env-driven 配置（所有微服务都读它） | 任何部署的起点 |
| [`pool-minimal.example.json`](pool-minimal.example.json) | 最小 `completion-service` 上游池——单 endpoint、无熔断 | 想从旧 `COMPL_ENDPOINT` 平滑过渡到 JSON 格式 |
| [`pool.example.json`](pool.example.json) | 完整 `completion-service` 上游池——多 endpoint、熔断、模型亲和 | 多上游容灾 / 跨厂商负载分担 / 大模型分流 |
| [`docker-compose.scale.example.yml`](docker-compose.scale.example.yml) | 微服务横向扩容 + pool 配置挂载 overlay | 高并发部署，单实例无法满足 QPS |

## 常用工作流

### 1. 首次启动

```bash
cp config/.env.example .env
# 编辑 .env 填入真实 API key、ADMIN_SECRET 等
docker compose up -d --build
```

### 2. 启用多上游池

```bash
cp config/.env.example .env
cp config/pool.example.json pool.json
# 编辑 pool.json：填真实 URL、调权重、配置熔断阈值
# 在 .env 中追加：
echo "COMPL_POOL_CONFIG_FILE=/etc/llm_gateway/pool.json" >> .env
echo "OPENAI_KEY_PRIMARY=sk-..." >> .env
echo "AZURE_KEY_FALLBACK=..." >> .env

# 让 docker-compose 把 pool.json 挂进 completion-service:
docker compose \
  -f docker-compose.yml \
  -f config/docker-compose.scale.example.yml \
  up -d --build
```

### 3. 横向扩容

参考 [`docker-compose.scale.example.yml`](docker-compose.scale.example.yml) 的头部注释，两种工作流都有完整命令。

### 4. 运行时调整（无需重启）

通过 admin API 在线改 weight / 启停 endpoint / 重置熔断器。详见 [`docs/api.md` §3.3](../docs/api.md#33-completion-上游池管理)。

## 配置项总览

每个 env 变量、JSON 字段的完整语义见 [`README.md` 「Configuration Reference」](../README.md#configuration-reference)。
