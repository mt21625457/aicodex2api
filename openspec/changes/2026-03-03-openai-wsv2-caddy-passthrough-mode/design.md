## Context

本变更目标是给 OpenAI WS v2 新增一个“纯透传模式”，将网关职责缩减为：

1. 认证替换（替换上游凭证）
2. 计费采集（被动解析 usage）

其余协议语义全部交由客户端与上游自行约束，不由网关干预。

当前实现存在较强语义介入（payload 归一、`previous_response_id` 推断/治理、tool output 恢复重放、会话状态依赖），与上述目标冲突，因此采用**新增模式并行实现**。

## Goals

1. 实现 OpenAI WS v2 passthrough 模式：除认证与计费外，语义零改写。
2. 复用 Caddy 双向隧道实现思想与代码结构，避免自研复杂状态机。
3. 保持现有 `ctx_pool` 路径不变，可按账号灰度切换。
4. 提供明确可观测性，证明“未改写语义”。

## Non-Goals

1. 不修复客户端缺失 `previous_response_id` 的业务问题。
2. 不做 `tool_output_not_found` 本地修复或自动重放。
3. 不引入多实例状态同步设计。

## Mode and Routing Design

### A. Ingress Mode 扩展

沿用现有 v2 路由开关，扩展模式值：

- `off`
- `ctx_pool`（现有增强语义路径）
- `passthrough`（新增纯透传路径）

账号解析优先级保持不变（新字符串字段 > 旧布尔字段 > 默认值），旧值 `shared/dedicated` 继续兼容到 `ctx_pool`。

### B. Forwarder 分支

在 `ProxyResponsesWebSocketFromClient` 入口增加早分支：

1. `off` -> 拒绝 WS mode
2. `ctx_pool` -> 走现有路径（不改）
3. `passthrough` -> 走新函数 `proxyResponsesWebSocketPassthroughV2(...)`

### C. 代码目录与模块边界（openai_ws_v2）

为降低改造风险并保持可维护性，新增目录约束：

1. OpenAI WS v2 passthrough 新增实现统一位于 `backend/internal/service/openai_ws_v2/`
2. 建议子模块：
   - `entry.go`：对外入口（接收 handler/forwarder 透传所需依赖）
   - `passthrough_relay.go`：双向帧转发与 idle timeout
   - `caddy_adapter.go`：Caddy streaming 思想/代码适配层
3. 现有 `backend/internal/service/openai_ws_forwarder.go` 保持稳定入口与 mode 分流，不承载新增 passthrough 细节
4. 历史 `ctx_pool` 逻辑本次不做目录迁移，避免大规模重构引入回归
5. `openai_ws_v2` 模块通过依赖注入获取外部能力（dial、usage、log、metrics、scheduler 输入），避免与 `internal/service` 产生循环依赖

### Handler 层分支说明

passthrough 分支在 handler 层**早于** Turn hooks 构建提前分流：

1. handler `ResponsesWebSocket` 检测到 `mode=passthrough` 后调用独立函数
2. 跳过 Turn 循环与 `BeforeTurn` 逻辑（不进入 turn-by-turn 调度）
3. 仅复用一个“连接结束回调”用于上报计费与调度结果（实现上沿用 `AfterTurn` 回调签名，触发一次）
4. 并发槽位获取/释放在 handler 层处理（非 hooks 内部），与 ctx_pool 路径解耦
5. passthrough 仍通过独立分支进入，`turnState/sessionStore` 等语义恢复参数不参与数据面

### 首包语义校验分流

现有 handler 在 mode 判断**之前**执行了两项语义校验（`openai_gateway_handler.go:617-627`）：

1. `model` 非空校验（空则拒绝连接）
2. `previous_response_id` 格式校验（message_id 格式则拒绝连接）

按 OpenAI WebSocket Mode 协议对齐后，分流策略调整为：

1. **model/previous_response_id 的提取保留**（`gjson.GetBytes` 只读操作，用于 `SelectAccountWithScheduler` 路由决策）
2. **model 非空校验作为最小前置校验保留**：`mode=passthrough` 且 `model` 为空时本地直接拒绝，不透传上游
3. **previous_response_id 格式校验仅 ctx_pool 执行**：passthrough 保留原值透传，不做本地修复
4. passthrough 继续保持“零语义改写”，但允许最小协议前置失败（空 model）以避免必失败上游请求

## Turn Model and Concurrency

### A. 全程隧道模式（无 Turn 概念）

passthrough 采用**全程隧道模式**，不分 Turn：

1. 连接建立后直接启动双向帧转发，直到任一方断开
2. **不调用** `hooks.BeforeTurn`
3. 可在连接结束时触发一次 `AfterTurn`（仅做上报，不参与 turn 循环）
4. **不依赖** Turn 计数器、Turn 级别的 usage 聚合

### B. 并发槽位管理

passthrough 的并发控制在**连接级别**而非 Turn 级别：

1. **获取时机**：连接建立时获取用户并发槽位 + 账号并发槽位
2. **释放时机**：连接断开时释放所有槽位（通过 `defer`）
3. **槽位类型**：复用现有 `accountConcurrency` 和 `userConcurrency` 槽位
4. 若获取失败，立即拒绝连接并返回 WS close（status 1013 Try Again Later）

### C. Handler 层分流

Handler 检测到 `mode=passthrough` 后调用独立函数，**早于** Turn hooks 构建提前分流：

1. handler `ResponsesWebSocket` 在模式判断后直接调用 passthrough 专用路径
2. 跳过 Turn 循环与 `BeforeTurn` 逻辑
3. 连接结束时可复用一次 `AfterTurn` 进行计费与调度上报
4. 并发槽位获取/释放在 handler 层处理（非 hooks），与 ctx_pool 路径完全解耦

## Connection Strategy

### A. 不使用 ingressCtxPool，但保留调度闭环

passthrough **不使用** `ingressCtxPool`（上下文连接池）：

1. 每次请求直接通过 `openAIWSClientDialer` 建立上游连接
2. 与现有调度链路保持一致：仍复用 `SelectAccountWithScheduler`（含 `groupID/sessionHash/model` 输入）
3. 会话粘性策略保持一致：允许执行 `BindStickySession`（调度缓存），避免与现网选路行为分叉
4. 上游连接生命周期与客户端连接绑定（一对一）
5. 禁用的是 WS 语义修复状态：`OpenAIWSStateStore` 的 `ResponseAccount/ResponseConn/ResponsePendingToolCalls/SessionTurnState/SessionLastResponseID/SessionConn` 读写

### B. 连接生命周期

1. 客户端 WS 升级完成 → 获取并发槽位 → dial 上游 → 启动双向隧道
2. 任一方断开 → 关闭另一方 → 释放并发槽位 → 记录 usage
3. 不做连接复用、不做连接池回收

## Passthrough Data Plane (Caddy-style Tunnel)

## Core Principle

复用 Caddy 的“升级完成后双向隧道”思想：连接建立后不做业务语义处理，仅做双向复制与生命周期管理。

### A. 连接建立

1. 下游连接：沿用当前 handler 已升级的 `coderws.Conn`
2. 上游连接：沿用当前 dialer 建立 OpenAI WS v2 连接
3. 认证：只在握手时写入上游 `Authorization: Bearer <token>`

### B. 双向复制

建立两个 goroutine：

1. client -> upstream：逐帧透传（保留 message type 与 payload）
2. upstream -> client：逐帧透传（保留 message type 与 payload）

任一方向退出即触发另一方向收敛关闭（对齐 Caddy `switchProtocolCopier` 行为）。

`openai_ws_v2` 目录内建议以“入口 + relay + 适配器”分层组织，避免新逻辑再次回流到 `openai_ws_forwarder.go`。

### WS Frame-Level Copy 澄清

Caddy `switchProtocolCopier` 是 TCP 级 `io.Copy`（因为已 hijack 底层连接），本实现与之不同：

1. 本实现使用 `coderws.Conn` 的 `Read/Write` 帧级 API（保留 message type: text/binary）
2. "Caddy 风格"仅指双 goroutine 并发模型和关闭收敛策略，**不**直接使用 `io.Copy`
3. 帧级操作的优势：能在 upstream -> client 方向对 text frame 做只读 usage 解析，而 binary frame 和 control frame 直接透传

### 上游连接帧级方法

现有 `openAIWSClientConn` 接口仅提供 `WriteJSON(ctx, value)` 和 `ReadMessage(ctx) → ([]byte, error)`，不保留 message type。passthrough 需要帧级（保留 message type）的读写能力。

改造方案：**不修改 `openAIWSClientConn` 接口**，仅在具体类型 `coderOpenAIWSClientConn` 上新增两个方法：

1. `ReadFrame(ctx) → (messageType coderws.MessageType, payload []byte, error)` — 保留帧类型的原始读取
2. `WriteFrame(ctx, messageType coderws.MessageType, payload []byte) → error` — 保留帧类型的原始写入

底层 `coderOpenAIWSClientConn` 已持有 `*coderws.Conn`，其 `Read(ctx)` 和 `Write(ctx, msgType, payload)` 原生支持帧级操作，实现为直接委托调用。

不改接口的好处：现有 mock（`openAIWSCaptureConn` 等）零改动，避免 `ctx_pool` 路径回归风险。`openai_ws_v2` 包内定义独立的 `FrameConn` 接口，service 层通过适配器桥接。

现有 `WriteJSON` / `ReadMessage` 方法保持不变（`ctx_pool` 路径继续使用），新增方法仅由 passthrough 路径调用。

### C. 超时与关闭策略

对齐 Caddy streaming 行为：

1. 支持 **idle timeout**（读空闲超时后关闭隧道）
2. 优雅关闭：尽力发送 WS close control，再关闭连接
3. 不做语义级重试与重放

#### Idle Timeout 配置来源

复用现有配置项 `gateway.openai_ws.read_timeout_seconds`（默认 900s）作为**读空闲超时（idle timeout）**：

1. 不新增额外配置项
2. 语义为 **idle timeout**：每次收到有效帧时重置读 deadline，超过 900s 无帧到达则触发优雅关闭
3. 与现有 `ctx_pool` 路径的 read deadline 行为一致
4. **注意与 Caddy `stream_timeout` 的区别**：Caddy `stream_timeout` 是绝对生命周期超时（从连接建立开始计时，不重置）；本实现不采用绝对超时模型，因为 passthrough 隧道可能长时间活跃

## Upstream Disconnect and Reconnect Policy

上游断开的处理策略：

1. **上游断开 → 直接终止客户端连接**（发送 close 或关闭连接，不做本地重放）
2. **不做任何重连或重试**
3. 客户端需自行处理重连逻辑
4. 断开时触发 `defer` 函数完成 usage 写入和槽位释放

理由：passthrough 模式的设计哲学是"协议层透传、零语义介入"，重连意味着网关需要重建上游会话状态，违背透传原则。

## Mode Hot-Switch Behavior

账号 ingress mode 切换的过渡行为：

1. **ctx_pool → passthrough**：已有连接继续使用 ctx_pool 直到自然结束，新连接走 passthrough
2. **passthrough → ctx_pool**：同理，已有 passthrough 连接继续直到自然结束
3. **模式在连接建立时决定**，连接生命周期内不变
4. 模式切换通过管理端更新账号配置生效，下次连接建立时读取最新配置

不需要特殊的"排水"机制：WS 连接自然结束时间受 `read_timeout_seconds` 约束（最长 900s）。

## Semantic Zero-Mutation Contract

当 mode=passthrough 时，以下逻辑必须禁用：

1. `parseClientPayload` 的 type/model/client_metadata 改写
2. `response.append` 拦截与改写建议
3. `previous_response_id` 注入、剥离、对齐
4. `store_disabled` proactive reject
5. `tool_output_not_found` / `previous_response_not_found` 恢复重放
6. 依赖 `OpenAIWSStateStore` 的链路修复读写（不含 scheduler/sticky cache）

注意：网关可做协议级安全校验（空消息、非 text/binary frame、连接状态），但不得修改业务 JSON 语义。

## OpenAI WS Mode Protocol Alignment

与 OpenAI WebSocket Mode 文档对齐的协议约束：

1. 单连接内不引入额外并发语义：网关不做多路复用、重排、并发 turn 合并；并发违规由上游协议语义裁决
2. `previous_response_id` 由客户端维护续链，passthrough 不做本地修复
3. 连接存在上游生命周期上限（文档示例为 60 分钟）：到期导致的 close/error 由网关原样下发，且不自动重连

## Billing-Only Side Channel

透传不等于不计费。计费通过“旁路解析”完成：

1. 在 upstream -> client 转发路径对消息做只读解析（`gjson.GetBytes`）
2. 提取 `response.completed` / `response.usage.*` 等 usage 字段
3. 转发字节不变，解析失败仅记录日志，不影响转发
4. 连接结束后按已采集 usage 记账（保持现有 `RecordUsage` 流程）

### Usage 数据丢失缓解

连接异常中断时的 usage 保护策略：

1. **best-effort 写入**：已解析但未入库的 usage 由 `defer` 函数触发 best-effort 写入
2. **解析范围**：`gjson.GetBytes` 仅对 text frame 做 usage 提取尝试，binary frame 和 control frame 跳过
3. **聚合级别**：usage 聚合器在连接级别维护（非 Turn 级别），连接断开时统一提交
4. **失败容忍**：若 `defer` 写入也失败（如数据库不可达），记录 error 日志后放弃，不阻塞连接清理

## Caddy Code Reuse Plan

## Source Baseline

- Upstream: `caddyserver/caddy`
- 推荐 pin：`f283062d37c50627d53ca682ebae2ce219b35515`（2026-03-02）
- 主要参考模块：
  - `modules/caddyhttp/reverseproxy/streaming.go`
  - `modules/caddyhttp/reverseproxy/reverseproxy.go`（仅流控/头处理思想）

### Adaptation Scope

直接采用并适配以下能力：

1. 双向 copy 协程模型
2. idle timeout 控制
3. websocket graceful close control
4. 连接注册与统一清理模式

不采用（因架构不匹配）：

1. `http.ResponseWriter` hijack 路径
2. HTTP hop-by-hop 清洗（本模式已在 WS 建链后）

### License Compliance

必须落地：

1. 保留原版权与许可证头
2. 仓库新增/更新 `THIRD_PARTY_NOTICES.md`
3. 在设计与代码注释中记录来源文件与 commit

## Observability

新增/补充指标：

1. `openai_ws_passthrough_sessions_active`
2. `openai_ws_passthrough_relay_errors_total{direction,reason}`
3. `openai_ws_passthrough_semantic_mutation_total`（期望恒为 0）
4. `openai_ws_passthrough_usage_parse_fail_total`

关键日志字段：

1. `ws_mode=passthrough`
2. `router_version=v2`
3. `semantic_mutation=false`
4. `relay_direction`、`wrote_downstream`

## Testing Strategy

### Unit

1. passthrough 模式下消息字节前后完全一致（golden payload，包括 function_call_output）。
2. passthrough 模式下不触发 `previous_response_id` 注入/移除逻辑。
3. passthrough 模式下不触发 recovery/replay 分支。
4. usage 旁路解析失败不影响透传链路。

### Integration

1. 客户端发送无 `previous_response_id` 的 function_call_output：网关不改写，直接透传上游错误/响应。
2. 上游 error event 原样到达客户端（字段不变）。
3. 客户端断连后按策略关闭上游，usage 采集不崩溃。

### Regression

1. `ctx_pool` 现有测试全通过，行为无回归。
2. `mode=passthrough` 与 `mode=ctx_pool` 互斥且可灰度切换。

## Rollout Plan

1. 代码上线但不切账号到 `passthrough`。
2. 单账号灰度（API Key 与 OAuth 各 1 个）。
3. 观察 24h：
   - `semantic_mutation_total` 必须为 0
   - `tool_output_not_found` 网关本地修复日志应在 passthrough 流量中归零
4. 逐步扩大账号范围；异常时把账号模式回切 `ctx_pool`。

### Rollout Automation

1. 指标读取接口：
   - `GET /api/v1/admin/ops/openai-ws-v2/passthrough-metrics`
2. 灰度守护脚本：
   - `tools/perf/openai_ws_v2_passthrough_gray_guard.py`
3. 灰度/回切编排脚本：
   - `tools/perf/openai_ws_v2_passthrough_rollout.py`
4. 回切策略：
   - 守护脚本退出码非 0 时，发布脚本自动将 canary 账号从 `passthrough` 回切到 `ctx_pool`。
