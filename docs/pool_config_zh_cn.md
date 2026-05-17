# Completion 上游池配置参考

[English](pool_config.md) | [中文](pool_config_zh_cn.md)

本文档是 `completion-service` 上游池的权威参考——这一层负责决定每个请求路由到哪个上游 LLM endpoint、如何重试、何时熔断、以及如何暴露运行时统计。

简要总览见项目根 [`README.md`](../README.md#completion-pool-json-multi-endpoint-mode)。运行时改池子用的 admin HTTP 接口见 [`docs/api.md` § 3.3](api.md#33-completion-上游池管理)。

---

## 1. 为什么需要池？

`completion-service` 历史上只能挂一个写死的上游（`COMPL_ENDPOINT`）。池层在同一个 gRPC 接口背后增加了：

- **多上游**——多个 OpenAI 兼容 endpoint（OpenAI、Azure OpenAI、自托管、混合厂商）按请求选择
- **加权分发**——按权重或按实时指标分配配额
- **模型亲和**——按模型把请求路由到真正提供该模型的厂商
- **熔断**——失败率超阈值的 endpoint 自动跳过，半开探测自动恢复
- **首字节前重试**——同步错误（拨号失败 / 非 2xx / 握手失败）时透明地切到另一 endpoint，全程发生在客户端收到任何字节之前
- **在线变更**——admin API 可以新增 / 移除 / 改权重 / 启停 endpoint，无需重启

更关键的是，这与**横向扩容是正交的**：etcd 服务发现把外部流量摊到 N 个 `completion-service` 副本，每个副本内部的池把工作摊到 M 个上游 endpoint。两个维度可以自由组合。

---

## 2. 配置来源

`completion-service` 按优先级顺序从下面**唯一一个**来源读取池配置：

| 优先级 | 来源 | 环境变量 | 说明 |
|---|---|---|---|
| 1（最高） | JSON 文件 | `COMPL_POOL_CONFIG_FILE` | JSON 文件路径（绝对路径或相对工作目录）。重新加载需要重启进程。 |
| 2 | 内联 JSON | `COMPL_POOL_CONFIG` | env 变量里的一个 JSON 字符串。在不想挂文件的 compose 部署里很好用。 |
| 3（最低） | 旧的单上游 | `COMPL_ENDPOINT` + `COMPL_API_KEY` | 合成一个单端点池；启动时打 deprecation 警告。 |

三种都没设 → 服务启动时直接报错退出。

启动日志示例：

```
[Info] pool: strategy=weighted_random max_attempts=3 endpoints=[openai-primary(w=3,enabled=true,breaker=closed) azure-fallback(w=1,enabled=true,breaker=closed)]
```

```
[Info] pool: COMPL_POOL_CONFIG_FILE/COMPL_POOL_CONFIG not set, falling back to legacy single-endpoint mode (COMPL_ENDPOINT)
[Info] pool: strategy=weighted_random max_attempts=1 endpoints=[legacy(w=1,enabled=true,breaker=disabled)]
```

加载逻辑实现在 `completion/pool/config.go:LoadConfigFromEnv()`。

---

## 3. 顶层 schema

```jsonc
{
  "strategy":     "weighted_random",   // 见 § 4
  "max_attempts": 3,                   // 见 § 5
  "breaker":      { ... },             // 可选；见 § 7
  "endpoints":    [ ... ]              // 必填；见 § 6
}
```

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `strategy` | string | `"weighted_random"` | 选择算法。允许值：`weighted_random`、`least_pending`、`ewma_latency`。 |
| `max_attempts` | int | `3` | 单个请求最多尝试的 endpoint 数。同请求内已试过的 endpoint 不会重试。 |
| `breaker` | object | 禁用 | 所有 endpoint 共享的熔断器设置。 |
| `endpoints` | array | — | **必填。** 至少一条；至少一条 `"enabled": true`。 |

> 解析器严格模式（`json.Decoder` 开了 `DisallowUnknownFields()`）：拼错任何字段名都会启动失败。JSON 不支持注释——如果需要写说明请用 sidecar `.md` 或 `_README` 字段（注意如果加了 `_README` 字段会因严格模式被拒绝；建议把注释完全放到 `.md` 文档里）。

---

## 4. 选择策略（`strategy`）

策略决定经过过滤器筛选后的候选集合中选哪个 endpoint 服务当前请求。

### 4.1 `weighted_random`（默认）

每个 endpoint 有一个正整数 `weight`。被选中的概率 = `weight / Σ(eligible endpoints 的 weight)`。无状态、公平。

适用场景：
- 你想要简单可预测的流量切分（"90% 厂商 A，10% 厂商 B"）
- 所有上游延迟分布接近
- 刚开始还没测过任何指标

### 4.2 `least_pending`

选 `InFlight`（当前在飞请求数）最小的 endpoint。`InFlight` 是 atomic 计数器，请求开始时 +1，stream channel 关闭时 -1。同分情况：先按 `weight` 高优先（高的赢），再按 name 字典序（保证确定性）。

适用场景：
- 上游延迟随负载波动很大
- 想要自适应分发但不想配置具体延迟阈值

### 4.3 `ewma_latency`

选 EWMA（指数加权移动平均）延迟最低的 endpoint，alpha=0.2。延迟单位是微秒，统计从请求开始到 channel 关闭。

**冷启动探测加成**：还没服务过任何请求的 endpoint（`LatencyUsEWMA == 0`）会被**优先**于有测量记录的 endpoint 选中——这保证新加的 endpoint 至少有机会被探一次，而不会被某个低延迟的 incumbent 永久饿死。

同分顺序：低延迟 → 高 weight → name 字典序。

适用场景：
- 想自动收敛到最快的活上游
- 厂商之间延迟差距明显（不同区域、不同模型）

---

## 5. 重试语义（`max_attempts`)

`max_attempts` 限定**单个请求**最多能试几个 endpoint。池的循环逻辑：

```
for attempt in 1 .. max_attempts:
    如果 ctx 已取消: 返回 ctx.Err()
    candidates = 跑 filters(snapshot)
    ep = selector.pick(candidates, 排除 tried)
    如果 ep 为 nil:
        返回错误
    把 ep 标记为 tried
    ch, err = call_upstream(ep)
    如果 err 为 nil: 返回 ch         # 成功——channel 交给调用方
    记录 err
返回 "exhausted" 包裹错误
```

单个请求里：
- 每个 endpoint **最多试一次**。失败后加入 `tried`，后续 pick 排除它。
- 上游同步错误（拨号失败、非 2xx、goroutine 启动前的 body 读失败）会触发切到另一 endpoint。
- 流式错误（channel 已经返回给调用方之后到达的 `chunk.Error`）**不会**重试，原样向客户端透传。原因见 § 9。

如果 `max_attempts` 大于 eligible endpoint 数，selector 在没有候选时直接返回 "no more endpoints"，循环提前退出，不会反复试已试过的 endpoint。

---

## 6. Endpoint schema（`endpoints[i]`）

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

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `name` | string | ✅ | 池内唯一标识符。admin API、日志、统计输出都用它。不能为空；必须唯一。 |
| `url` | string | ✅ | 完整的 chat completions URL。`net/url` 必须能解析。不做任何路径加工——完全按上游要求填，Azure 需要 `?api-version=...`。 |
| `api_key_env` | string | ✅ | **环境变量的名称**，不是 key 本身。completion-service 进程必须设了这个 env；openai client 每次请求时读它。这层间接的目的是把密钥与配置文件解耦。 |
| `weight` | int | ✅ | 必须 `> 0`。`weighted_random` 直接用；`least_pending` / `ewma_latency` 用作 tie-breaker。 |
| `models` | string 数组 | ❌ | 缺省 / 空数组 / `["*"]` 表示接受任何模型。否则只有请求里 `model` 字段精确匹配列表里某个值时才路由到此。**不**支持 glob / regex。 |
| `enabled` | bool | ✅ | `false` 时所有 selector 跳过。Stats 和 breaker 状态会保留，方便 admin 再启用时不丢历史。 |

### 为什么用 `api_key_env` 而不是 `api_key`？

1. **配置文件不夹密钥**：`pool.json` 可以放到配置仓库或挂载自 configmap 而不泄漏 key。
2. **进程内注入**：在 Docker / k8s 里，env var 是标准密钥通路（Docker secrets、k8s Secret -> envFrom）。
3. **多 endpoint 共享同一 key**：两个 endpoint 共用一个 OPENAI key 时都引用同一个 env 变量名即可。

请求落到该 endpoint 时，openai client 执行 `os.Getenv(api_key_env)`。如果 env 变量缺失或为空，上游调用会 401 失败 → 池切到另一 endpoint → 失败计入熔断计数。**加载器不在启动时校验 env 是否存在**——这是有意的，让启动快，并容忍那些在进程启动后才注入的密钥。

---

## 7. 熔断器（`breaker`）

熔断由 [`sony/gobreaker`](https://github.com/sony/gobreaker) 支撑。**每个 endpoint 一个独立 breaker**，状态不共享。禁用时（默认），`breaker_state` 报 `"disabled"`，所有 filter 逻辑短路为「通过」。

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

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `enabled` | bool | `false` | `false` 时不构造 breaker。所有 `BreakerState` snapshot 都报 `"disabled"`。 |
| `max_requests` | uint32 | `1` | 半开状态允许放行的试探请求数。第一个成功 → 关闭；第一个失败 → 再开。 |
| `interval` | duration 字符串 | `"60s"` | closed 状态滚动计数器的滑动窗口。每个 interval 边界重置计数。设 `"0"` 表示永远累计（基本没用）。 |
| `timeout` | duration 字符串 | `"30s"` | open 状态持续多久后转半开。 |
| `failure_ratio` | float (0..1] | `0.5` | 每个请求失败完成时评估 `failures / requests >= failure_ratio`，达到就开。 |
| `min_requests` | uint32 | `5` | 当前 interval 内至少累计这么多请求后，failure_ratio 才会被评估。防止小样本抖动。 |

duration 字符串使用 Go 的 `time.ParseDuration` 语法：`"500ms"`、`"30s"`、`"5m"`。启用时如果 duration 写错，启动会失败。

**状态机**：

```
closed ─(失败率越阈值)──► open
  ▲                          │
  │       (半开里任何成功)    │
  │                          ▼
  └────────────────────── half-open ─(任何失败)─► open
```

**状态变更日志**（Info 级别）：

```
[Info] pool: breaker "openai-primary" closed -> open
[Info] pool: breaker "openai-primary" open -> half-open
[Info] pool: breaker "openai-primary" half-open -> closed
```

**什么算失败？** 只有 `upstreamClient.GetStream` 的同步错误——也就是触发重试的同一组错误。流中错误（`chunk.Error`）**不**计入 breaker 计数器。原因见 § 9。

### 手动重置 breaker

admin API：`POST /admin/completion/breaker/reset` 带 `{"name":"..."}`。breaker 用相同配置重建，计数器和状态回到 `closed`。详见 [`docs/api.md` § 3.3](api.md#33-completion-上游池管理)。

---

## 8. 过滤链

每次选 endpoint 之前，候选集合按顺序过下面两个 filter：

1. **`model_affinity`**——丢掉 `models` 列表不接受请求 `model` 的 endpoint。空列表或 `["*"]` 通配匹配任意。请求的 `model` 字段就是 OpenAI 标准请求里的那个字段，不做任何加工。
2. **`breaker_open`**——丢掉 breaker 处于 `StateOpen` 的 endpoint。半开状态会被放过（让试探请求能跑）。

如果 filter 把候选清空，selector 返回「无可用 endpoint」，重试循环以错误终止。常见原因：
- 所有 endpoint 都被禁用（admin 关掉了，或初始配置全是 `enabled: false`）
- 所有 endpoint 的 breaker 同时打开了（相关性故障）
- 请求 `model` 谁都不匹配——通常是配置错或客户端 `model` 写错

---

## 9. 流式路径上的错误 / 重试边界

池的重试边界**严格在 channel 交给调用方之前**。一旦 `pool.GetStream` 返回了 channel，gateway 就会向客户端打开 SSE 流，客户端可能已经在读字节——这时候重试要么会重复输出（如果从新上游再发一遍），要么会卡死（如果等重试静默成功）。

上游侧的 openai client（`completion/openai/openai.go`）：

```
1.  http.Do(...)                    — 同步,可能返 err → 重试
2.  if status != 200: return err    — 同步,可能返 err → 重试
3.  spawn goroutine 读 SSE
4.  return ch, nil                  — channel 交给调用方
5.  goroutine: ch <- 第一个 chunk   — 第一个字节给客户端,重试已不可能
```

步骤 1-2 的错误从 `pool.callEndpoint` 以 `(nil, err)` 形式返回；重试循环试另一个 endpoint。步骤 4 之后的错误以 `chunk.Error` 形式从返回的 channel 抵达，原样透传给客户端。

这是有意的权衡：
- ✅ 首字节延迟保持低——不需要额外往返来「校验」流
- ❌ 「200 但立即报错」的上游响应（实际很罕见）会成为不重试的、直接抛给客户端的错误

如果你的上游有较高的流中失败率，未来可以考虑（Phase 5+）：在返回 channel 前同步 peek 第一个 chunk 来扩大重试窗口。

---

## 10. 校验规则

加载器在启动时拒绝以下任意一项：

- `endpoints` 为空
- 两个 endpoint 同 `name`
- 任意 endpoint 的 `name`、`url`、`api_key_env` 为空
- 任意 endpoint 的 `url` 被 `net/url.Parse` 拒绝
- 任意 endpoint 的 `weight <= 0`（注意：`weight` 完全省略时默认为 `1`，但显式的 `0` 或负值会被拒）
- 没有任何 endpoint 是 `enabled: true`
- `strategy` 不是三种之一
- `breaker.enabled: true` 且 `breaker.interval` / `breaker.timeout` 无法解析

加载器自动规整：
- `strategy` 为空 → `weighted_random`
- `max_attempts <= 0` → `3`
- `breaker.failure_ratio <= 0 或 > 1` → `0.5`
- `breaker.min_requests == 0` → `5`
- `breaker.max_requests == 0` → `1`

校验代码在 `completion/pool/config.go:validate()`。

---

## 11. 实战示例

### 11.1 两厂商 75% 主 / 25% 备，无熔断

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

### 11.2 按模型路由

GPT-4o 走 OpenAI，Claude 走 Anthropic 适配器，其他走通用 catch-all：

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

> 注意：当请求 `model` **只**匹配 `catchall`（通过 `["*"]`）时，`weighted_random` 仍然只在通过 `model_affinity` 的候选里选——本例中就是 `catchall` 自己。catch-all 不会削弱精确匹配请求的选择，因为 filter 在 selector 之前跑。

### 11.3 延迟感知 + 熔断

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
    { "name": "us-east",  "url": "...", "api_key_env": "K1", "weight": 1, "enabled": true },
    { "name": "eu-west",  "url": "...", "api_key_env": "K2", "weight": 1, "enabled": true },
    { "name": "ap-tokyo", "url": "...", "api_key_env": "K3", "weight": 1, "enabled": true }
  ]
}
```

每个区域独立跟踪延迟，流量自然聚到最快的区。某区抽风时，~10 次 >50% 失败率后该区熔断打开，流量立刻避开，每 30s 用 1 次试探请求探测恢复。

### 11.4 从旧的 `COMPL_ENDPOINT` 迁移

旧 `.env`：
```bash
COMPL_ENDPOINT=https://api.openai.com/v1/chat/completions
COMPL_API_KEY=sk-abc123
```

最小改动等价 JSON（`config/pool-minimal.example.json` 就是这个）：
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

跑通后再渐进添加 fallback endpoint、把 `max_attempts` 提到 2 或 3、最后开熔断。

---

## 12. 运行时变更

`endpoints[*]` 的每个字段都能通过 gateway admin API 在运行时改：

| 操作 | 接口 | 请求体 |
|---|---|---|
| 列表 | `GET /admin/completion/endpoints` | — |
| 新增 | `POST /admin/completion/endpoint` | EndpointSpec |
| 移除 | `DELETE /admin/completion/endpoint` | `{"name":"..."}` |
| 改权重 | `POST /admin/completion/endpoint/weight` | `{"name":"...", "weight": N}` |
| 启用 / 禁用 | `POST /admin/completion/endpoint/enabled` | `{"name":"...", "enabled": true|false}` |
| 重置 breaker | `POST /admin/completion/breaker/reset` | `{"name":"..."}` |

实现细节（`completion/pool/admin.go`）：
- 所有变更都拿写锁，对 endpoints slice 做 **copy-on-write**。`Stats` 和 `Breaker` 指针在重建中保留，所以 `Reweight` / `SetEnabled` 不会丢计数器和熔断状态。
- 在飞请求持有 `GetStream` 入口处抓的 snapshot；admin 变更不影响它们，正常读完。
- `AddEndpoint` 跑和启动加载器一样的校验。
- `ResetBreaker` 在池启动时是 `breaker.enabled: false` 的情况下返回 `424 Failed Dependency`——没东西可重置。

完整 HTTP 请求 / 响应形状见 [`docs/api.md` § 3.3](api.md#33-completion-上游池管理)。

---

## 13. 多副本语义

池是**进程内状态**。当 `completion-service` 在 etcd 服务发现后跑 N 个副本时：
- 每个副本启动时读相同的 `COMPL_POOL_CONFIG_FILE` → 初始池一致
- 每个副本**各自**维护 breaker 计数、EWMA 延迟、在飞计数
- admin 变更落到 **一个** 副本（gateway gRPC round-robin 选中的那个）

实际意味着：
- `/admin/completion/stats` 返回的统计只反映一个副本。要看整集群，自行在外部聚合多个副本。
- `Reweight` 调用只影响接到 RPC 的那个副本。要全局生效，要么：(a) 调 N 次 admin（gateway round-robin 不保证均匀覆盖——非确定性），要么 (b) 改配置文件然后 `docker compose restart completion-service` 触发权威重载。
- breaker 状态是本地的。某副本 `endpoint-a` 的 `open` 状态**不会**阻止其他副本继续试 `endpoint-a`。一般没事——相关性故障会让每个副本的 breaker 各自在几秒内独立 trip。

未来可以加 etcd watch 来广播变更，但当前实现不做。取舍是：池保持简单、本地；把配置文件视为集群权威；admin RPC 留给战术性的单副本探测 / 覆盖。

---

## 14. 常见问题 / 坑

**Q: 启动时报 `pool: endpoint "x" url invalid: ...`。**
URL 字符串过不了 `net/url.Parse`。检查是否有未转义字符、缺 `https://`、或多余空白。Azure URL 需要 `?api-version=...` 但前面的路径必须精确。

**Q: 我用 admin 新加了 endpoint，但从来没被选中。**
最大概率：`models` 列表不匹配请求里的 `model` 字段。跑 `GET /admin/completion/endpoints` 确认。也检查请求是不是被熔断器过滤掉了（看 `breaker_state`）。

**Q: 我设了 `min_requests: 5`，但 1 次失败就 trip 了。**
gobreaker 的 `min_requests` 是在**当前 interval** 内评估的。如果流量稀疏，可能 interval 边界滚过来时累计还不够 5 条。把 `interval` 调大（如 `"5m"`）或把 `min_requests` 调小。

**Q: 两个 endpoint 共享同一个 key——要写两个 `api_key_env` 吗？**
不用。两个 endpoint 可以引用同一个 env 变量名；env 变量在进程里只需要设一次。重复的 `api_key_env` 值没问题。

**Q: 怎么不重新部署就 rollout 一个配置变更？**
改 JSON 文件 → 全网 `docker compose restart completion-service`。重启很快（每副本亚秒级）且优雅（在飞请求 drain）。admin API 改动是战术性的，不适合作永久状态变更。

**Q: 我通过 admin 禁用了一个 endpoint；重启后会回来吗？**
会。重启重读 `COMPL_POOL_CONFIG_FILE`，那是真理源。admin 变更只在内存中。

**Q: gateway 可以不配池就跑吗？**
不行——`completion-service` 必须至少有一种来源设上，否则启动报错。旧的 `COMPL_ENDPOINT` 模式是最简单的回退。

---

## 15. 代码索引

| 关注点 | 文件 |
|---|---|
| 加载器、env 优先级、校验 | `completion/pool/config.go` |
| 池服务、重试循环、channel 包装 | `completion/pool/pool.go` |
| Endpoint 类型、内部 client 接口 | `completion/pool/endpoint.go` |
| `weighted_random` 选择器 | `completion/pool/selector.go` |
| `least_pending` 选择器 | `completion/pool/selector_lp.go` |
| `ewma_latency` 选择器 | `completion/pool/selector_ewma.go` |
| 过滤器（model affinity、breaker open） | `completion/pool/filter.go` |
| 熔断器配置与工厂 | `completion/pool/breaker.go` |
| 统计计数与 EWMA | `completion/pool/stats.go` |
| 运行时变更（admin） | `completion/pool/admin.go` |
| gRPC 服务端 / 客户端（Service、StatsProvider、Admin） | `completion/grpc/server.go`、`completion/grpc/admin_server.go`、`completion/grpc/client.go` |
| Gateway admin HTTP handler | `gateway/admin_pool.go` |
