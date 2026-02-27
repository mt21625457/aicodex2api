## Why

针对 OpenAI WSv2 热路径与连接池，我们复核了 20 项审查点。当前代码中存在一组可直接证明的问题：

- 高热路径存在可避免分配：`normalizeOpenAIWSLogValue` 每次创建 `strings.NewReplacer`。
- 连接生命周期不一致：`error` 事件后在部分分支未标记连接损坏。
- 写上游超时不继承父 `context`，客户端断连后仍可能阻塞到默认超时。
- `BindResponseAccount` 错误被静默忽略，粘连异常缺少可观测性。
- ingress WS 非首轮 turn 并发参数与首轮调度参数不一致。
- 协议决策器对未知认证类型缺少显式回退。
- 连接池缺少后台健康探测与后台清理，仅在 `Acquire` 被动触发。
- 连接 I/O 读写共用同一互斥锁，限制全双工并发能力。
- 代理 `Transport` 缺少 `TLSHandshakeTimeout`。
- Redis 状态读写缺少独立短超时，异常时可能拖长请求。
- 事件循环在客户端断连后仍执行非必要处理。
- 入站 WS 在客户端断连时未继续 drain 上游，行为与 HTTP-SSE 不一致。
- 消息读取上限过高（128MB），存在内存风险。

## What Changes

本变更统一落地以下优化：

1. `forwarder` 热路径与连接安全
- 将日志值归一化替换器提升为包级变量。
- 收到 `error` 事件后一律 `MarkBroken()`。
- `BindResponseAccount` 失败输出 `warn` 日志。
- 客户端已断连后，跳过 model/tool 修正等非必要处理，仅保留必要解析。
- usage 解析增加事件类型快速门控（仅 `response.completed`）。
- ingress WS 客户端断连时继续 drain 上游至 terminal，不再立刻打断并污染连接复用。

2. `pool` 并发与后台维护
- `writeJSONWithTimeout` 支持继承父 `context`（新增 `WriteJSONWithContextTimeout`）。
- 连接 I/O 锁拆分为 `readMu/writeMu`，支持并发一读一写。
- 新增后台 ping worker（30s）探测所有空闲连接。
- 新增后台 cleanup worker（30s）定期扫描所有账号池。
- `queueLimitPerConn` 兜底默认值从 `256` 下调为 `16`。

3. `client/handler/protocol/state_store` 可靠性与资源保护
- 代理 `http.Transport` 增加 `TLSHandshakeTimeout: 10s`。
- WS 读上限从 `128MB` 下调为 `16MB`（客户端与 ingress 入口一致）。
- ingress 非首轮 turn 统一使用调度器确定的并发参数。
- 协议决策器对未知认证类型显式回退 HTTP。
- `OpenAIWSStateStore` 对 Redis `set/get/delete` 增加独立 3s 超时包装。

## Deferred (已确认但本次不直接改)

- terminal 后“尾包探测”方案：直接 probe read 会对 `coder/websocket` 连接状态产生副作用，需改为更安全机制后再落地。
- prewarm `creating` 计数语义重构：涉及扩容/预热协同策略，需要独立压测验证。
- `replaceOpenAIWSMessageModel` 的双 `sjson.SetBytes` 深度优化：需在正确性与性能之间进一步权衡。
- `GetResponseAccount` Redis 命中后本地回填：需先定义“无陈旧读”一致性边界。

## Capabilities

### Modified Capabilities

- `openai-ws-v2-performance`

## Impact

- 影响模块：
  - `backend/internal/service/openai_ws_forwarder.go`
  - `backend/internal/service/openai_ws_pool.go`
  - `backend/internal/service/openai_ws_client.go`
  - `backend/internal/service/openai_ws_state_store.go`
  - `backend/internal/service/openai_ws_protocol_resolver.go`
  - `backend/internal/handler/openai_gateway_handler.go`
- 影响类型：热路径性能、连接池稳定性、异常可观测性。
- 兼容性：外部 API 与协议保持不变。

