---
phase: proposal-openai-ws-v2-codex-cli-ctx-pool
plan: "01"
type: execute
wave: 1
depends_on: []
files_modified:
  - backend/internal/service/account.go
  - backend/internal/service/openai_ws_protocol_resolver.go
  - backend/internal/service/openai_ws_forwarder.go
  - backend/internal/service/openai_ws_ingress_context_pool.go
  - backend/internal/config/config.go
  - backend/internal/config/config_test.go
  - backend/internal/service/openai_ws_protocol_resolver_test.go
  - backend/internal/service/openai_ws_forwarder_ingress_session_test.go
autonomous: true
requirements:
  - WSV2-CTX-001
  - WSV2-CTX-002
  - WSV2-CTX-003
must_haves:
  truths:
    - 仅 OpenAI Responses WebSocket v2 且 Codex CLI 入站时启用 ctx_pool 模式
    - 每个 Codex CLI WS 连接绑定一个 context，单连接多 turn 使用同一上游 WS
    - context 绑定账号维度（group_id + account_id + session_hash），不会跨账号复用
    - 其他模式（off/shared/dedicated）行为保持不变
  artifacts:
    - 新增 ctx_pool 模式常量与路由分支
    - 新增 ingress context pool 实现（获取、释放、重建、TTL 回收）
    - 新增日志字段用于判定是否进入 ctx_pool 模式
    - 新增测试覆盖模式生效边界与账号粘连
  key_links:
    - account.ResolveOpenAIResponsesWebSocketV2Mode -> protocol resolver -> ingress forwarder
    - ingress ctx_pool acquire/release -> 客户端断开 -> context 回池
    - session_hash + account_id 粘连 -> context key -> 上游连接复用
---

<objective>
实现一个新的 WS ingress 模式 `ctx_pool`，用于修复 Codex CLI 在 OpenAI WS v2 下的 continuation 连接不稳定问题。

Purpose: 以“每个客户端 WS 强绑定 context”的方式，降低跨连接续链失败（1008 continuation unavailable）概率。
Output: 一条可灰度发布、可回滚、对 legacy 模式零侵入的实现方案与改造任务清单。
</objective>

<context>
约束与边界（来自本次需求）:
1. 每个 Codex CLI WS 连接 bind 一个 context。
2. 如果该 context 的上游 WS 出问题，直接在该 context 内重建，不走普通连接池抢连接。
3. 客户端断开后将 context 放回 context 池；后续同 session 优先取回。
4. 按账号并发限制 context 数量，且必须考虑账号粘连。
5. 该逻辑仅适用于 OpenAI WebSocket mode v2，其他模式不走此路径。
</context>

<tasks>

<task type="auto">
  <name>Task 1: 增加 ctx_pool 模式定义与路由开关</name>
  <files>backend/internal/service/account.go, backend/internal/service/openai_ws_protocol_resolver.go, backend/internal/config/config.go, backend/internal/config/config_test.go</files>
  <action>
新增 `OpenAIWSIngressModeCtxPool = "ctx_pool"`，并纳入 mode normalize/validate。
在 protocol resolver 中允许 `ctx_pool` 进入 `responses_websockets_v2` 路由。
保持默认值不变（dedicated），确保不影响现有部署。
  </action>
  <verify>go test ./backend/internal/config -run "TestLoadDefaultOpenAIWSConfig|TestValidateConfig_OpenAIWSRules" -count=1</verify>
  <done>配置可识别 ctx_pool 且 shared/dedicated/off 回归行为不变。</done>
</task>

<task type="auto">
  <name>Task 2: 实现 ingress context pool（账号粘连 + 会话复用）</name>
  <files>backend/internal/service/openai_ws_ingress_context_pool.go</files>
  <action>
新增 `openAIWSIngressContextPool`：
- key 维度：`group_id + account_id + session_hash`。
- 生命周期：Acquire(绑定) -> Use(多 turn) -> Release(客户端断开回池) -> TTL 回收。
- 连接异常：context 标记 broken，下一次在同 context 重建上游 WS。
- 资源上限：每账号 context 数量 == account.concurrency（硬约束，例如并发 20 则池容量 20）。

账号粘连规则：context 只能在同 account_id 下复用；不同账号即使 session_hash 相同也不允许命中。
  </action>
  <verify>新增单测：同账号同 session 重连复用；跨账号不复用；超并发返回 busy。</verify>
  <done>context pool 可独立工作，具备回池、重建和上限保护。</done>
</task>

<task type="auto">
  <name>Task 3: 在 WSv2 ingress forwarder 挂接 ctx_pool 分支</name>
  <files>backend/internal/service/openai_ws_forwarder.go</files>
  <action>
仅在以下条件满足时进入 ctx_pool 分支：
- transport == responses_websockets_v2
- ingress_mode == ctx_pool
- request 是 Codex CLI（User-Agent 或已存在识别逻辑）

其他情况全部走原有逻辑，不改行为。
将 turn 循环中的 acquire/release 接口抽象为 lease-like 适配，支持原连接池和 context pool 双实现。
  </action>
  <verify>go test ./backend/internal/service -run "TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_.*CtxPool.*" -count=1</verify>
  <done>ctx_pool 分支可跑通首包、续链、多 turn、断开回池流程。</done>
</task>

<task type="auto">
  <name>Task 4: 增加可观测性与灰度控制</name>
  <files>backend/internal/handler/openai_gateway_handler.go, backend/internal/service/openai_ws_forwarder.go</files>
  <action>
新增日志字段：
- `openai_ws_ctx_pool_mode`（是否进入 ctx_pool）
- `openai_ws_ctx_pool_key`（脱敏）
- `openai_ws_ctx_reused` / `openai_ws_ctx_rebuild`
- `openai_ws_ctx_account_sticky_hit`

并输出拒绝原因（非 Codex CLI / 非 WSv2 / mode 不匹配）。
  </action>
  <verify>本地联调日志可明确判断“为何进入/未进入 ctx_pool”。</verify>
  <done>线上排障可直接定位模式、会话、账号粘连命中情况。</done>
</task>

<task type="auto">
  <name>Task 5: 回归与发布策略</name>
  <files>backend/internal/service/openai_ws_protocol_resolver_test.go, backend/internal/service/openai_ws_forwarder_ingress_session_test.go</files>
  <action>
补充回归：
- shared/dedicated/off 原行为不变
- ctx_pool 只在 Codex CLI + WSv2 生效
- continuation unavailable 场景下 context 内重建成功率验证

发布采用灰度：先给单账号开启 `..._responses_websockets_v2_mode: "ctx_pool"`，观察错误率后逐步放量。
  </action>
  <verify>目标错误下降：1008 continuation unavailable 与首包失败率下降。</verify>
  <done>具备灰度、回滚、观测闭环。</done>
</task>

</tasks>

<verification>
Before declaring plan complete:
- [ ] `ctx_pool` 模式仅在 WSv2 + Codex CLI 生效
- [ ] 同账号同 session 断线重连可回收并复用 context
- [ ] 账号粘连生效（不跨 account）
- [ ] shared/dedicated/off 回归测试通过
- [ ] 错误日志可区分 acquire 失败、context 重建、模式不匹配
</verification>

<success_criteria>
- continuation unavailable 频率显著下降（以账号灰度样本对比）。
- flate 首包错误不因本提案新增逻辑而放大。
- ctx_pool 开启与关闭可由账号模式字段控制，回滚无需改代码。
- 现网非 ctx_pool 账号完全不受影响。
</success_criteria>

<rollback>
1. 将账号 `..._responses_websockets_v2_mode` 从 `ctx_pool` 改回 `dedicated/shared`。
2. 保留代码不删除，通过配置切回原路径。
3. 如需紧急回滚，全局可启用 `force_http=true`。
</rollback>
