---
phase: proposal-openai-ws-v2-gateway-forwarding-audit-remediation
plan: "01"
type: execute
wave: 1
depends_on: []
files_modified:
  - backend/internal/service/openai_ws_forwarder.go
  - backend/internal/service/openai_ws_forwarder_ingress_session_test.go
  - backend/internal/service/openai_ws_forwarder_success_test.go
  - backend/internal/config/config.go
  - backend/internal/config/config_test.go
  - backend/internal/service/account.go
  - backend/internal/service/account_openai_passthrough_test.go
  - backend/internal/handler/openai_gateway_handler.go
  - deploy/config.example.yaml
autonomous: true
requirements:
  - WS-AUDIT-001
  - WS-AUDIT-002
  - WS-AUDIT-003
  - WS-AUDIT-004
must_haves:
  truths:
    - WebSocket v2 ingress 在上游返回 `type=error` 事件后必须立即结束当前 turn，不能等待 read timeout。
    - 上游 `type=error` 仅终止当前 turn；客户端 WS 会话可继续下一 turn（由网关重建上游连接）。
    - OAuth WSv2 握手头中 `user-agent` 与 `originator` 必须保持 Codex 语义一致。
    - 配置层必须禁止 `responses_websockets=true` 且 `responses_websockets_v2=false` 的 v1-only 误配置。
    - dedicated ingress 模式允许会话结束后健康连接回池复用，避免无必要重建。
  artifacts:
    - 修复 `openai_ws_forwarder` 错误事件终止、续链恢复与握手头逻辑
    - 补充配置校验与默认值说明
    - 补充单元测试覆盖上述关键行为
    - 提供灰度发布和回滚策略
  key_links:
    - OpenAI WS ingress turn loop -> upstream relay -> terminal handling
    - buildOpenAIWSHeaders -> OAuth forced Codex UA -> originator
    - config.Validate -> WSv2 guard
---

<objective>
基于现有改动，完成 OpenAI WebSocket mode v2 网关转发链路修复提案，形成可直接执行与验收的闭环。

Purpose: 把审核发现的问题收敛为“已修复 + 可验证 + 可灰度”的方案，降低续链失败和握手不一致风险。
Output: 一个可用于实现跟踪和发布评审的执行提案（计划 + 设计 + 验证 + 回滚）。
</objective>

<context>
本提案以当前工作区已有改动为基线，不回滚、不重写历史。

现状快照：
1. 代码已覆盖核心修复（error 事件即时终止、OAuth 握手头对齐、WSv1-only 配置拦截）。
2. 已补齐关键单测并通过 `go test ./internal/config`、`go test ./internal/service`。
3. 当前剩余工作重点是“发布前收敛”：明确链路行为、风险边界、灰度检查与回滚。
</context>

<tasks>

<task type="auto">
  <name>Task 1: 锁定 ingress turn 错误终止语义</name>
  <files>backend/internal/service/openai_ws_forwarder.go, backend/internal/service/openai_ws_forwarder_ingress_session_test.go</files>
  <action>
确认并冻结以下行为：上游 `type=error` 事件一旦下发给客户端，立即 `MarkBroken + return` 结束 turn。
补充注释与测试断言，确保不会再次回退到“等待下一次上游 read 触发超时”的旧行为。
同时确保该错误只结束当前 turn，不主动断开客户端 WS；后续 turn 可在新上游连接继续。
  </action>
  <verify>go test ./backend/internal/service -run "TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_ErrorEventTerminatesTurnWithoutReadTimeoutWait" -count=1</verify>
  <done>错误事件触发后可在秒级结束当前 turn，且同一客户端 WS 可继续下一轮。</done>
</task>

<task type="auto">
  <name>Task 2: 锁定 OAuth 握手头一致性</name>
  <files>backend/internal/service/openai_ws_forwarder.go, backend/internal/service/openai_ws_forwarder_success_test.go</files>
  <action>
保持规则：当 OAuth 流程强制使用 Codex UA 时，必须同步设置 `originator=codex_cli_rs`。
避免出现 `user-agent` 与 `originator` 语义不一致，减少上游策略拒绝与追踪歧义。
  </action>
  <verify>go test ./backend/internal/service -run "TestOpenAIGatewayService_Forward_WSv2_OAuthAlignOriginatorWithForcedCodexUA|TestOpenAIGatewayService_Forward_WSv2_OAuthStoreFalseByDefault" -count=1</verify>
  <done>OAuth WSv2 握手头稳定对齐，日志与行为可预测。</done>
</task>

<task type="auto">
  <name>Task 3: 配置层禁止 v1-only 误配置</name>
  <files>backend/internal/config/config.go, backend/internal/config/config_test.go, deploy/config.example.yaml</files>
  <action>
在 Validate 中拒绝 `responses_websockets=true && responses_websockets_v2=false`。
同步默认值文档：`mode_router_v2_enabled=true`、`ingress_mode_default=dedicated`。
  </action>
  <verify>go test ./backend/internal/config -run "TestLoadDefaultOpenAIWSConfig|TestValidateConfig_OpenAIWSRules" -count=1</verify>
  <done>部署配置不会进入“看似开启 WS 但实际不支持”的陷阱。</done>
</task>

<task type="auto">
  <name>Task 4: 续链恢复路径回归</name>
  <files>backend/internal/service/openai_ws_forwarder.go, backend/internal/service/openai_ws_forwarder_ingress_session_test.go</files>
  <action>
锁定优先续链失败后的恢复顺序：
1) 从状态存储回填 preferred conn；
2) 仍失败则降级移除 `previous_response_id` 并重放完整输入。

确保 dedicated 会话结束后连接回池，可让后续会话命中 `response_id -> conn_id` 续链。
  </action>
  <verify>go test ./backend/internal/service -run "TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_FirstTurnPreferredConnUnavailableRecoversByDropPreviousResponseID|TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_DedicatedModeReusesConnAcrossSessionsWhenHealthy" -count=1</verify>
  <done>续链失败可自动恢复，且连接复用策略与会话级绑定兼容。</done>
</task>

<task type="auto">
  <name>Task 5: 发布收敛与灰度验收</name>
  <files>backend/internal/handler/openai_gateway_handler.go</files>
  <action>
基于已有日志字段进行灰度观测：
- `openai_ws_mode_router_v2_enabled`
- `openai_ws_ingress_mode`
- `openai_ws_dedicated_mode`

按账号灰度开启，追踪：
- continuation unavailable 关闭率
- ingress 首包失败率
- WS turn 平均完成时延

若异常，优先回退账号模式，再按需 `force_http=true` 全局兜底。
  </action>
  <verify>灰度样本中上述核心错误率下降且未引入新超时峰值。</verify>
  <done>实现“可观测、可回滚、可逐步放量”。</done>
</task>

</tasks>

<verification>
Before declaring plan complete:
- [ ] error 事件路径不会等待 read timeout
- [ ] OAuth 握手头 `user-agent` / `originator` 一致
- [ ] v1-only 配置会被 Validate 拒绝
- [ ] dedicated 模式跨会话健康连接可复用
- [ ] `internal/config` 与 `internal/service` 全包测试通过
</verification>

<success_criteria>
- 网关 WSv2 ingress 错误事件处理时延从“超时级”收敛到“即时返回级”。
- OAuth 握手头不再出现 Codex UA 与非 Codex originator 混搭。
- 配置发布阶段可提前阻断不支持的 WSv1-only 组合。
- 续链恢复成功率提升，且 dedicated 模式不出现无谓重拨。
</success_criteria>

<rollback>
1. 按账号将 `responses_websockets_v2_mode` 从 `dedicated/ctx_pool` 回切到 `shared/off`。
2. 如需网关级快速止血，设置 `gateway.openai_ws.force_http=true`。
3. 保留代码，仅通过配置回退路径，避免紧急代码回滚风险。
</rollback>
