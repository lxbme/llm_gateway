# 分布式部署指南

[English](distributed_deployment.md) | [中文](distributed_deployment_zh_cn.md)

| 字段 | 取值 |
|---|---|
| **状态** | 稳定 |
| **读者** | 在多主机环境中部署 llm-gateway 的运维人员 |
| **范围** | 基于 etcd 的服务发现、跨主机配置、网络要求 |
| **参见** | [`pool_config_zh_cn.md`](pool_config_zh_cn.md)、[`api.md`](api.md)、[`gateway_working_mechanism.md`](gateway_working_mechanism.md) |

---

## 1. 引言

### 1.1 概要

本文档描述如何将 llm-gateway 微服务套件（`gateway`、`embedding-service`、`cache-service`、`completion-service`、`auth-service`、`rag-service`）部署到多台主机上。对于生产级部署，本文取代仓库内 `docker-compose.prod.yml` 所代表的单机 Compose 方案。

单机 Compose 栈依赖 Docker 内建桥接网络的服务名 DNS 进行服务间寻址。在多主机拓扑下该网络不可用，必须将服务发现委托给一个 `etcd` 集群，并令每个实例对外通告 LAN 内可路由的地址。

### 1.2 文档约定

本文档采用以下约定：

- **指令（Directive）** — 一个或多个服务读取的环境变量。
- **是否必填** — 在所描述的拓扑下该指令是否必须设置。
- **默认值** — 未设置时所采用的取值。
- **作用域** — 读取该指令的服务集合。
- 示例主机以 `host-a`、`host-b` 等命名；LAN 地址（`10.0.1.10` … `10.0.1.13`）仅用于示意。
- 代码块采用 POSIX shell 或 YAML 语法。必填占位符以 `<like-this>` 形式书写。

### 1.3 先决条件

- 一个 LAN（或虚拟覆盖网络），所有参与主机能够在第 5 节列出的端口上互通。
- 每台运行微服务的主机上已安装容器运行时（Docker 或 Podman）。
- completion 池中每个 provider 至少有一个可访问的上游 LLM 端点，详见 [`pool_config_zh_cn.md`](pool_config_zh_cn.md)。
- 一种可共享的密钥分发机制（环境文件、密钥管理服务、或编排器原生 secret），用于分发 API key 与 `ADMIN_SECRET`。

---

## 2. 架构概览

### 2.1 服务组件

| 组件 | 角色 | 默认端口 | 是否有状态 |
|---|---|---|---|
| `gateway` | HTTP 入口；聚合 auth、cache、RAG、completion | 8080（公网）、8081（admin） | 否 |
| `embedding-service` | 文本嵌入（代理上游嵌入 provider） | 50051 | 否 |
| `cache-service` | 语义缓存查找与回写 | 50052 | 否（依赖 Qdrant） |
| `completion-service` | 上游 LLM 池、流式、重试、熔断 | 50053 | 否（仅有进程内池状态） |
| `auth-service` | API key 签发、校验、限流计账 | 50054 | 否（依赖 Redis） |
| `rag-service` | 文档检索与入库 | 50055 | 否（依赖 Qdrant） |
| `qdrant`（第三方） | 缓存与 RAG 共用的向量库 | 6333（HTTP）、6334（gRPC） | 是 |
| `redis`（第三方） | auth 使用的持久化键值存储 | 6379 | 是 |
| `etcd`（第三方） | 服务注册与配置协调 | 2379（client）、2380（peer） | 是 |

只有 `qdrant`、`redis`、`etcd` 持有持久状态。六个 llm-gateway 微服务均为无状态进程，可任意重启、扩缩容或迁移而不丢失数据。

### 2.2 服务发现模型

注册与解析机制实现于 `internal/discovery/`，支持两种工作模式：

1. **发现启用** — 设置了 `ETCD_ENDPOINTS`。每个服务向 etcd 写入键 `services/<service-name>/<instance-id>`，其值为该实例的 `ADVERTISE_ADDR`，由 10 秒 lease 持续续约。客户端通过 gRPC resolver `etcd:///services/<service-name>` 解析端点列表，并使用 `round_robin` 负载均衡。
2. **直连模式** — 未设置 `ETCD_ENDPOINTS`。gateway 通过对应的 `*_ADDR`（`CACHE_ADDR`、`COMPL_ADDR` 等）直连各后端。该模式仅用于单机本地开发。

多主机部署必须使用发现启用模式。

### 2.3 地址通告

启用发现后，实例向 etcd 注册的地址由 `internal/discovery/register.go` 决定：

```
ADVERTISE_ADDR（环境变量） → 若已设置，按字面值使用
                          → 否则使用 os.Hostname() + ":" + <fallback-port>
```

主机名回退在单 Docker 桥接网络内有效，因为桥接内建 DNS 能解析容器名。但跨主机时容器主机名不可解析；`ADVERTISE_ADDR` 必须显式设置为 LAN 内可路由的 `host:port`。

`ADVERTISE_ADDR` 配错是多主机拓扑下服务间连接失败的最常见原因。可用以下命令核对：

```bash
etcdctl --endpoints=<etcd-host>:2379 get /services/ --prefix
```

返回的每个 value 都必须能从 gateway 主机直接访问。

---

## 3. 部署拓扑

### 3.1 单主机（参考）

仓库提供 `docker-compose.prod.yml` 作为自包含的单主机部署示例。该方案适用于本地验证与演示，不在本文档范围内。

### 3.2 分布式

分布式部署至少将整套服务切分为三种主机角色：

| 层级 | 组件 | 典型主机数 |
|---|---|---|
| **边缘层** | `gateway` × N，入口负载均衡器（nginx、HAProxy、云 LB） | 1–N |
| **无状态计算层** | `embedding-service`、`cache-service`、`completion-service`、`auth-service`、`rag-service` | 每个服务 1–M |
| **数据层** | `etcd` 集群（3 或 5 节点）、`qdrant`、`redis` | 1–N |

如果不同服务的请求量差异显著，可在无状态计算层进一步按服务分片。例如以 completion 为主的工作负载可以采用 3× `completion-service` + 各 1× 其他服务的配比。

### 3.3 混合

在评估环境中，各层级可以合并部署。第 6 节给出了一个最简的三主机分布式部署示例。

---

## 4. 配置参考

### 4.1 通用指令

下列指令被所有 llm-gateway 微服务读取。

#### `ETCD_ENDPOINTS`

| 字段 | 取值 |
|---|---|
| **说明** | 以逗号分隔的 etcd 客户端端点列表（`host:port`）。非空时启用服务发现。 |
| **是否必填** | 多主机部署下必填。 |
| **默认值** | *（未设置）* — 关闭发现，仅通过 `*_ADDR` 直连。 |
| **作用域** | 所有服务。 |
| **示例** | `ETCD_ENDPOINTS=10.0.1.10:2379,10.0.1.11:2379,10.0.1.12:2379` |

生产环境应列出 etcd 集群所有节点，使客户端连接在任一节点失联时仍可使用。

#### `ADVERTISE_ADDR`

| 字段 | 取值 |
|---|---|
| **说明** | 实例向 etcd 注册的 `host:port`。其他服务据此地址发起连接。 |
| **是否必填** | 多主机部署下必填。 |
| **默认值** | `os.Hostname() + ":" + <fallback-port>` — 仅在单 Docker 桥接网络内有效。 |
| **作用域** | 所有服务。 |
| **示例** | `ADVERTISE_ADDR=10.0.1.11:50053`（host-b 上的 completion-service） |

该取值必须能从所有运行 `gateway` 的主机解析并访问。`127.0.0.1` 永远不合法；容器内部主机名跨主机不可用。

`docker-compose.prod.yml` 识别一组 wrapper 变量 `EMBEDDING_ADVERTISE_ADDR` / `CACHE_ADVERTISE_ADDR` / `COMPLETION_ADVERTISE_ADDR` / `AUTH_ADVERTISE_ADDR` / `RAG_ADVERTISE_ADDR`；在容器内部，进程实际读取的仍然是名为 `ADVERTISE_ADDR` 的标准变量。

#### `SERVE_PORT`

| 字段 | 取值 |
|---|---|
| **说明** | 进程内 gRPC 服务（对 `gateway` 而言是 HTTP 服务）监听的端口。 |
| **是否必填** | 否 |
| **默认值** | 每个服务的默认端口，见 2.1 节。 |
| **作用域** | 所有服务。 |
| **示例** | `SERVE_PORT=50063`（在同一主机上并列两个 completion-service 实例时使用） |

当多个同类服务实例以 `network_mode: host` 模式并列部署于同一主机时，必须为每个实例分配不同的 `SERVE_PORT`，并使 `ADVERTISE_ADDR` 使用同一端口。

### 4.2 `gateway`

| 指令 | 是否必填 | 说明 |
|---|---|---|
| `ETCD_ENDPOINTS` | 是 | 见 4.1 节。 |
| `ADMIN_SECRET` | 是 | `/admin/*` 路由所需的 `X-Admin-Secret` 头共享密钥。 |
| `DEBUG_MODE` | 否 | 开启请求详细日志。默认 `false`。 |
| `LOG_LEVEL` | 否 | `ERROR`、`WARN`、`INFO`、`DEBUG` 之一。默认 `ERROR`。 |
| `CACHE_ADDR`、`COMPL_ADDR`、`AUTH_ADDR`、`RAG_ADDR` | 否 | 直连兜底地址。设置了 `ETCD_ENDPOINTS` 后忽略。 |

gateway 是唯一应暴露到数据平面 LAN 之外的服务。Admin 端口（8081）必须仅绑定 `127.0.0.1`，远程管理通过 SSH 隧道访问（见 7.4 节）。

### 4.3 `embedding-service`

| 指令 | 是否必填 | 说明 |
|---|---|---|
| `ETCD_ENDPOINTS` | 是 | 见 4.1 节。 |
| `ADVERTISE_ADDR` | 是 | LAN 内可达的 `host:50051`（或其他 `SERVE_PORT`）。 |
| `EMBED_PROVIDER` | 是 | 上游 provider 标识符（如 `openai`）。 |
| `EMBED_API_KEY` | 是 | provider API key。 |
| `EMBED_ENDPOINT` | 是 | provider HTTPS 端点 URL。 |
| `EMBED_MODEL` | 是 | 模型名称。 |
| `EMBED_DIMENSIONS` | 是 | 嵌入维度（整数）。 |

### 4.4 `cache-service`

| 指令 | 是否必填 | 说明 |
|---|---|---|
| `ETCD_ENDPOINTS` | 是 | 见 4.1 节。 |
| `ADVERTISE_ADDR` | 是 | LAN 内可达的 `host:50052`。 |
| `QDRANT_HOST` | 是 | Qdrant 实例的主机名或 IP。 |
| `QDRANT_PORT` | 否 | 默认 `6334`（gRPC）。 |
| `QDRANT_COLLECTION_NAME` | 否 | 默认 `llm_semantic_cache`。 |
| `QDRANT_SIMILARITY_THRESHOLD` | 否 | `[0,1]` 之间浮点数。默认 `0.95`。 |
| `CACHE_MODE` | 否 | `semantic` 或 `exact`。默认 `semantic`。 |
| `CACHE_STORE_PROVIDER` | 否 | 默认 `qdrant`。 |
| `CACHE_BUFFER_SIZE` | 否 | 默认 `1000`。 |
| `CACHE_WORKER_COUNT` | 否 | 默认 `5`。 |

多主机模式下不再读取 `EMBED_ADDR`；embedding 服务通过 etcd 解析。

### 4.5 `completion-service`

| 指令 | 是否必填 | 说明 |
|---|---|---|
| `ETCD_ENDPOINTS` | 是 | 见 4.1 节。 |
| `ADVERTISE_ADDR` | 是 | LAN 内可达的 `host:50053`。 |
| `COMPL_POOL_CONFIG_FILE` | 条件性必填 | 容器内描述上游池的 JSON 文件路径，详见 [`pool_config_zh_cn.md`](pool_config_zh_cn.md)。 |
| `COMPL_POOL_CONFIG` | 条件性必填 | 内联 JSON，当 `COMPL_POOL_CONFIG_FILE` 未设置时使用。 |
| `COMPL_ENDPOINT`、`COMPL_API_KEY` | 条件性必填 | 旧版单端点模式；当上述两个池指令均未设置时使用。分布式部署中已弃用。 |
| *（provider 特有的 key 环境变量）* | 是 | 池 JSON 中 `api_key_env` 字段所引用的变量。 |

池配置在进程启动时一次性读取。通过 admin API（7.4 节及 [`api.md`](api.md) § 3.3）进行的运行时变更仅影响接收该请求的那一个实例，详见 8.2 节。

### 4.6 `auth-service`

| 指令 | 是否必填 | 说明 |
|---|---|---|
| `ETCD_ENDPOINTS` | 是 | 见 4.1 节。 |
| `ADVERTISE_ADDR` | 是 | LAN 内可达的 `host:50054`。 |
| `REDIS_ADDR` | 是 | Redis 实例的 `host:port`。 |
| `REDIS_DB` | 否 | 默认 `0`。 |

### 4.7 `rag-service`

| 指令 | 是否必填 | 说明 |
|---|---|---|
| `ETCD_ENDPOINTS` | 是 | 见 4.1 节。 |
| `ADVERTISE_ADDR` | 是 | LAN 内可达的 `host:50055`。 |
| `QDRANT_HOST` | 是 | Qdrant 实例的主机名或 IP。 |
| `QDRANT_PORT` | 否 | 默认 `6334`。 |
| `QDRANT_COLLECTION_NAME` | 否 | 默认 `llm_rag_documents`。 |
| `RAG_SIMILARITY_THRESHOLD` | 否 | 默认 `0.6`。 |
| `RAG_DEFAULT_TOP_K` | 否 | 默认 `3`。 |

embedding 依赖通过 etcd 解析，无需 `EMBED_ADDR`。

---

## 5. 网络要求

### 5.1 端口映射

| 组件 | 监听端口 | 协议 | 调用方 |
|---|---|---|---|
| `etcd` | 2379 | HTTP（client） | 所有 llm-gateway 服务 |
| `etcd` | 2380 | HTTP（peer） | 其他 etcd 节点 |
| `qdrant` | 6334 | gRPC | `cache-service`、`rag-service` |
| `redis` | 6379 | RESP | `auth-service` |
| `embedding-service` | 50051 | gRPC | 其他 llm-gateway 服务 |
| `cache-service` | 50052 | gRPC | `gateway` |
| `completion-service` | 50053 | gRPC | `gateway` |
| `auth-service` | 50054 | gRPC | `gateway` |
| `rag-service` | 50055 | gRPC | `gateway` |
| `gateway` | 8080 | HTTP | 入口负载均衡器 |
| `gateway` | 8081 | HTTP（admin） | 仅 `127.0.0.1` |

llm-gateway 服务的端口为每个实例独占。同主机并列多个实例时，必须按 4.1 节（`SERVE_PORT`）分配额外端口。

### 5.2 防火墙规则

每种主机角色的最小入站规则集：

- **etcd 主机** — 允许来自所有 llm-gateway 主机的 2379/tcp；仅允许来自 peer etcd 主机的 2380/tcp。
- **Qdrant 主机** — 允许来自 `cache-service` 与 `rag-service` 主机的 6334/tcp。
- **Redis 主机** — 允许来自 `auth-service` 主机的 6379/tcp。
- **微服务主机** — 允许来自所有 `gateway` 主机的对应服务端口（5.1 节）。
- **Gateway 主机** — 允许来自入口负载均衡器的 8080/tcp。
- **入口** — 允许公网（或客户端来源网段）的 80/tcp 与 443/tcp。

任何 `gateway` 主机的 8081 端口管理访问必须限制为本机回环。远程管理使用 SSH 本地端口转发，详见 7.4 节。

### 5.3 地址通告规则

1. `ADVERTISE_ADDR` 必须能从所有调用方主机解析。
2. `ADVERTISE_ADDR` 不得为 `127.0.0.1`、`localhost`，亦不得为绑定到与调用方不共享的网络命名空间上的地址。
3. 使用 `network_mode: host` 时，`ADVERTISE_ADDR` 必须与主机 IP 及监听的 `SERVE_PORT` 一致。
4. 使用 Docker 桥接网络并显式发布端口时，`ADVERTISE_ADDR` 必须反映宿主机端口而非容器内部端口。

---

## 6. 完整示例：四主机分布式部署

下例演示横跨四台主机的拓扑：

| 主机 | 地址 | 组件 |
|---|---|---|
| host-a | `10.0.1.10` | `etcd-1`、`qdrant`、`redis` |
| host-b | `10.0.1.11` | `etcd-2`、`embedding-service`、`completion-service` × 2 |
| host-c | `10.0.1.12` | `etcd-3`、`cache-service`、`rag-service`、`auth-service` |
| host-d | `10.0.1.13` | `gateway` × 2、`nginx` |

### 6.1 etcd 集群启动

host-a 上：

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

在 host-b 与 host-c 上复制此配置，改写 `--name` 与对应的 peer URL。首次引导时三个节点必须在选举超时窗口内先后启动。

### 6.2 host-b 上的无状态服务

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

两个 completion-service 实例的唯一区别是 `ADVERTISE_ADDR` 与 `SERVE_PORT`。二者均注册到 `services/completion-service/` 下，由 gateway 的 `round_robin` 策略负载均衡。

### 6.3 host-d 上的 gateway 与入口

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

最小化的 `nginx.conf` upstream 片段：

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

`proxy_buffering off` 是必需项，否则流式 completion 响应会被 nginx 缓存直至上游 EOF。

---

## 7. 运维流程

### 7.1 启动顺序

1. 在 host-a、host-b、host-c 上启动 etcd 集群。用 `etcdctl --endpoints=... endpoint health` 确认三节点全部 healthy。
2. 在 host-a 启动 `qdrant`，确认 `curl http://10.0.1.10:6333/healthz` 返回 OK。
3. 在 host-a 启动 `redis`，确认 `redis-cli -h 10.0.1.10 ping` 返回 `PONG`。
4. 启动各无状态服务。彼此间无顺序要求，etcd 接受任意顺序的注册。
5. 验证发现状态：`etcdctl get /services/ --prefix` 应返回每个运行实例的一条键，且取值均为 LAN 可达的 `host:port`。
6. 在 host-d 上启动各 `gateway` 实例。
7. 启动 `nginx`。
8. 发起探针请求：`curl https://api.example.com/v1/health`。

### 7.2 验证

分布式部署的端到端验证：

```bash
# 1. 确认所有服务已注册
etcdctl --endpoints=10.0.1.10:2379 get /services/ --prefix --keys-only

# 2. 确认 gateway 能够访问每个后端
curl -sS https://api.example.com/v1/health

# 3. 发起一次完整的 completion 请求
curl -sS https://api.example.com/v1/chat/completions \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"ping"}]}'

# 4. 检查某个 gateway 实例上的池统计
ssh host-d -L 8081:127.0.0.1:8081 -N &
curl -H "X-Admin-Secret: $ADMIN_SECRET" http://127.0.0.1:8081/admin/completion/stats | jq
```

### 7.3 滚动重启

无状态服务在不丢请求的前提下重启：

1. 向目标实例发送 `SIGTERM`。服务的 deregister 钩子（`internal/discovery/register.go`）会主动删除其 etcd 键，gateway 在一个 resolver tick 内停止向其分发新请求。
2. 等待在飞请求自然结束（非流式通常数秒内完成，流式 completion 时间更长）。
3. 以同一 `ADVERTISE_ADDR` 启动替换实例。etcd 重新注册立即完成；gateway 在下一个 resolver tick 内拾取新地址。

如果未走 `SIGTERM`（被 `-9` 杀死），etcd lease 会在 10 秒后过期（`internal/discovery/register.go` 中的 `leaseTTLSeconds`）；该窗口内 gateway 可能仍向死地址路由并观测到连接错误。

### 7.4 远程管理

每台 gateway 主机的 admin 端口（8081）均绑定 `127.0.0.1`。远程管理通过 SSH 本地端口转发完成：

```bash
ssh -L 8081:127.0.0.1:8081 -N operator@host-d
# 另开一个终端：
curl -H "X-Admin-Secret: $ADMIN_SECRET" http://127.0.0.1:8081/admin/completion/endpoints
```

涉及 completion 池的管理操作（增 / 删 / 改权 / 禁用 / breaker reset）只影响接收请求的那一个实例，详见 8.2 节。

---

## 8. 已知限制

### 8.1 服务间流量未加密

llm-gateway 服务之间的 gRPC 连接使用 insecure credentials。在可信 LAN 上可以接受。跨非可信网络（多区域、跨可用区公有云）部署时，应将服务间流量隧道化（WireGuard、IPsec、或 service mesh sidecar）。当前版本未实现原生 TLS。

### 8.2 Admin 变更仅作用于本实例

对 completion 池的运行时变更仅传播到接收该 admin RPC 的那一个 `completion-service` 实例。系统无广播机制。如需全网生效：

- 在每台主机上更新 `COMPL_POOL_CONFIG_FILE`，然后执行滚动重启（7.3 节）；或
- 通过直连地址逐个调用每个实例，绕过 etcd 的 `round_robin` 策略。

### 8.3 有状态依赖存在单点

示例拓扑使用单节点 `qdrant` 与 `redis`。生产高可用方案应替换为集群或托管版本（Redis Sentinel/Cluster、Qdrant Cloud、或自建 Qdrant 集群）。

### 8.4 Lease 过期窗口

实例非优雅退出时，其 etcd 键会保留至 10 秒 lease 过期。窗口期内 gateway 的 `round_robin` balancer 仍可能命中死地址。建议生产调用方实现客户端重试。

---

## 9. 参见

- [`pool_config_zh_cn.md`](pool_config_zh_cn.md) — Completion 池配置参考。
- [`api.md`](api.md) — HTTP API 参考，包含 admin 端点。
- [`gateway_working_mechanism.md`](gateway_working_mechanism.md) — gateway 内部请求流程。
- [`../config/docker-compose.scale.example.yml`](../config/docker-compose.scale.example.yml) — 单主机横向扩容的 Compose 示例。
- [`README_zh_cn.md`](README_zh_cn.md) — 项目概览、快速开始、配置索引（中文）。
