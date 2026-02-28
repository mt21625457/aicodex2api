# OpenAI WebSocket Mode v2 网关转发链路修复设计（基于现有改动）

## 1. 背景

本设计对应本次审核目标：
1. 对齐 OpenAI WebSocket Mode 文档语义（WSv2 事件流、错误处理、续链预期）。
2. 对照 Codex CLI 真实握手习惯（Codex UA + originator）。
3. 审核并修复网关在 WS ingress 转发链路中的关键缺陷。

本稿件基于当前工作区已有改动汇总，不回滚已完成修复。

## 2. 链路基线

WSv2 ingress 主链路：
1. `handler.ResponsesWebSocket` 接收客户端 WS，读取首包。
2. 调度选账号，解析 transport 和 ingress mode。
3. `ProxyResponsesWebSocketFromClient` 获取/复用上游连接并转发 turn。
4. relay 上游事件到客户端，直至 terminal 或异常。
5. 会话结束释放 lease，连接回池（健康）或标记 broken（异常）。

关键协同点：
- `stateStore` 维护 `response_id -> conn_id` 粘连用于 continuation。
- mode router v2 控制 `off/shared/dedicated/ctx_pool` 分支（本轮重点为 dedicated）。

## 3. 审核问题与修复方案

### P0: 上游 `type=error` 事件后未立即终止 turn

问题：
- 旧逻辑在收到上游错误事件后仍可能继续等待下一次 upstream read。
- 在上游不再发送终止事件时，流程会拖到 read timeout（默认可达数百秒），表现为“网关假死”。

修复（已实现）：
- 在 relay 循环中识别 `type=error` 后，先转发错误事件到客户端，再立即：
  - `lease.MarkBroken()`
  - `return wrapOpenAIWSIngressTurnError("upstream_error_event", ...)`
- 语义：错误事件即当前 turn 的终止信号。
- 在外层 turn 循环中对 `upstream_error_event` 采用“turn 级失败”处理：
  - 当前 turn 调用 `AfterTurn(..., err)`；
  - 释放并重建上游连接；
  - 客户端 WS 不关闭，允许直接进入下一 turn。

验证：
- `TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_ErrorEventTerminatesTurnWithoutReadTimeoutWait`
- 使用 `openAIWSBlockAfterErrorConn` 模拟“发一条 error 后阻塞”，并验证下一 turn 可在新上游连接上继续。

### P1: OAuth 握手头 `user-agent` 与 `originator` 可能不一致

问题：
- OAuth 路径会将 UA 强制为 Codex 风格，但 `originator` 可能仍为非 Codex 值。
- 这会造成上游识别和审计口径分裂，增加握手拒绝或行为分叉风险。

修复（已实现）：
- `buildOpenAIWSHeaders` 中新增规则：
  - 若 OAuth 且最终 UA 为 Codex 样式，则强制 `originator=codex_cli_rs`。

验证：
- `TestOpenAIGatewayService_Forward_WSv2_OAuthStoreFalseByDefault`
- `TestOpenAIGatewayService_Forward_WSv2_OAuthAlignOriginatorWithForcedCodexUA`

### P1: 配置允许落入“v1-only 但系统实际不支持”陷阱

问题：
- `responses_websockets=true` 与 `responses_websockets_v2=false` 的组合可被设置，但行为不可用。

修复（已实现）：
- `Config.Validate()` 增加硬校验，拒绝上述组合。
- 同步默认值：
  - `mode_router_v2_enabled=true`
  - `ingress_mode_default=dedicated`

验证：
- `TestLoadDefaultOpenAIWSConfig`
- `TestValidateConfig_OpenAIWSRules`

### P1: dedicated 模式跨会话连接复用策略不合理

问题：
- 会话级强绑定本应由 `sessionLease` 保障，若每次都 `ForceNewConn=true`，会导致无谓重拨并降低续链命中。

修复（已实现）：
- dedicated 模式下不再强制每次新建连接。
- 会话结束后健康连接可回池，供后续会话复用；异常连接仍标记 broken。

验证：
- `TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_DedicatedModeReusesConnAcrossSessionsWhenHealthy`

### P2: continuation 连接不可用时缺少可恢复兜底

问题：
- 首 turn 依赖 `previous_response_id` 时，若 preferred conn 缺失，会直接失败。

修复（已实现）：
- 首先尝试从 `stateStore` 回填 sticky `conn_id` 重试。
- 若仍不可用，自动移除 `previous_response_id`，并重放完整输入重试。

验证：
- `TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_FirstTurnPreferredConnUnavailableRecoversByDropPreviousResponseID`

## 4. 关键实现决策

1. 错误事件“即时终止”优先于“等待 terminal 事件”
- 目标是快速失败而非长时间悬挂。

2. 握手头一致性优先于保留历史兼容杂糅
- Codex UA 与 Codex originator 同步，避免上游策略歧义。

3. 会话级隔离与连接池复用并存
- `sessionLease` 保证单会话不串流；回池保证跨会话效率。

4. 配置层前置失败
- 在启动/加载配置时阻断无效组合，减少线上灰色状态。

## 5. 剩余风险与后续建议

风险 A（已关闭）：error 事件后当前客户端 WS 是否应继续保活
- 本轮已改为“turn 失败、会话不断开”语义，并新增对应回归测试。

风险 B：灰度期间日志维度可能不足
- 当前已有 mode/router 字段；若需更细粒度排障，可追加 `acquire_reason`、`preferred_rehydrate_hit` 等字段。

建议：
1. 先按账号小流量灰度，重点观察 continuation unavailable 与 turn 失败时延。
2. 若确认需要“会话不断开”语义，再做第二阶段协议行为改造。

## 6. 测试与验证结果

已执行：
- `go test ./internal/config`
- `go test ./internal/service`

结果：
- 两个包测试均通过。

## 7. 发布与回滚

发布步骤：
1. 保持默认配置上线。
2. 按账号灰度开启目标 ingress mode。
3. 观察错误率、重试率、turn 时延后逐步放量。

回滚步骤：
1. 账号级回切到 `shared/off`。
2. 必要时开启 `gateway.openai_ws.force_http=true` 全局兜底。
