## Context

本次改动聚焦“确认且可安全上线”的 WSv2 运行时优化，目标是降低热路径开销并减少连接脏状态复用风险。

## Goals

- 在不改外部协议的前提下，降低 WSv2 高并发场景 CPU/分配和首包抖动。
- 提升连接生命周期一致性，减少死连接/脏连接进入复用池。
- 提高异常可观测性，避免 silent failure。

## Non-Goals

- 不重构调度器主流程。
- 不在本次引入新的外部依赖或持久化结构。
- 不改变 WS 默认开启策略。

## Decisions

### 1) Forwarder 热路径与错误处理

- 采用包级 `strings.Replacer`，消除每条日志重复构建开销。
- `error` 事件统一 `MarkBroken`，避免不可回退分支把异常连接放回池。
- 客户端断连后进入“最小处理”路径：跳过 model/tool 修正，仅保留必要状态推进。
- usage 解析改为事件门控，减少高频 token 事件的无效 JSON 查找。
- ingress WS 客户端断连后继续读上游直到 terminal，与 HTTP-SSE drain 语义对齐。

### 2) Pool 并发模型与后台维护

- 写操作超时统一继承父 `context`，避免断链请求占住写超时窗口。
- 读写锁拆分，恢复一读一写并发能力。
- 增加后台 worker：
  - ping worker：每 30s 探测空闲连接，失败即回收。
  - cleanup worker：每 30s 扫描全部账号池，执行过期/空闲清理。
- 兜底队列上限下调，避免配置缺失时出现极端长排队。

### 3) 入口与依赖保护

- 降低 WS 读上限至 16MB，收敛异常消息内存风险。
- 代理 transport 增加 TLS 握手超时，防止代理链路卡死。
- 协议决策器对未知认证类型显式回退 HTTP。
- Redis 状态读写统一包裹 3s 超时，避免长连接上下文下的阻塞外溢。

## Validation

- 单测新增/更新：
  - 协议决策未知认证回退。
  - StateStore Redis 独立超时。
  - Pool 写超时继承父 `context`。
  - Pool 读写并发与后台 sweep 行为。
  - Client TLSHandshakeTimeout。
- 定向回归：
  - `go test ./internal/service -run "OpenAIWS|ProxyResponsesWebSocketFromClient|Forward_WSv2|ProtocolResolver|StateStore"`
  - `go test ./internal/handler -run "OpenAI|websocket|Responses"`

## Risks & Mitigations

- 风险：后台 worker 增加周期性开销。
  - 缓解：只处理空闲连接；失败处理走现有 `evict`。
- 风险：断连后的最小处理路径影响日志可读性。
  - 缓解：保留关键断连日志与终态统计。

