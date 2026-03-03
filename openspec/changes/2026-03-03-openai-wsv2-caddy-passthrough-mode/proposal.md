## Why

当前 `openai ws mode v2` ingress 路径是“语义增强代理”，不是“纯透传代理”：

1. 会修改请求语义（如 `type/model/client_metadata/previous_response_id`）。
2. 会执行本地恢复与重放（`tool_output_not_found` / `previous_response_not_found`）。
3. 会依赖会话状态存储来做链路修复。

这与“只做计费与认证替换，其余语义完全透传”的目标不一致，也会引入额外复杂度与不可预期行为。

## What Changes

本提案新增 **OpenAI WS v2 Passthrough Mode**，采用 Caddy 的升级后双向隧道思路，构建“协议层透传、业务层零改写”的新路径。

### 1. 新增透传模式（v2 路由内）

在现有 `off|ctx_pool` 基础上新增 `passthrough`：

- 全局默认：`gateway.openai_ws.ingress_mode_default=off|ctx_pool|passthrough`
- 账号级（按类型）：
  - `accounts.extra.openai_oauth_responses_websockets_v2_mode`
  - `accounts.extra.openai_apikey_responses_websockets_v2_mode`

### 2. 透传模式行为边界（硬约束）

当 `effective_mode=passthrough` 时：

1. **仅允许两类处理**  
   - 认证替换（网关 token -> 上游 `Authorization`）  
   - 被动计费（从上游事件提取 usage，不改写事件）
2. **禁止语义改写**  
   - 不注入、不删除、不修正 `previous_response_id`
   - 不改 `type/model/input/tools/client_metadata`
   - 不做 tool output 预检与本地修复
3. **禁止恢复重放逻辑**  
   - 不做 replay、不做 turn 级重试、不做 error-event 后语义补救
4. **透传失败策略**  
   - 上游返回什么就转发什么；网关不做语义兜底包装

### 3. 采用 Caddy 代码路径（改造式复用）

直接引入 Caddy `reverseproxy/streaming` 的核心隧道实现模式（双 goroutine 双向拷贝 + stream timeout + 优雅关闭），并在本项目做最小适配。

### 4. 许可与可追溯

新增第三方代码归档与 NOTICE，记录 Caddy 源码来源与 commit pin，满足 Apache-2.0 合规要求。

## Non-Goals

1. 不在该模式下实现 `previous_response_id` 自动补齐或 `call_id` 反查修复。
2. 不在该模式下保留 `ctx_pool/store_disabled` 语义恢复能力。
3. 不替换现有 `ctx_pool` 路径；仅新增可选模式并行存在。

## Capabilities

### Added Capabilities

- `openai-ws-v2-passthrough`

## Impact

- 预期影响模块：
  - `backend/internal/config/config.go`
  - `backend/internal/config/config_test.go`
  - `backend/internal/service/account.go`
  - `backend/internal/service/openai_ws_protocol_resolver.go`
  - `backend/internal/service/openai_ws_forwarder.go`
  - `backend/internal/service/openai_ws_passthrough_relay.go`（新增）
  - `backend/internal/service/openai_ws_passthrough_relay_test.go`（新增）
  - `backend/internal/handler/openai_gateway_handler.go`（仅模式路由与 hooks 对齐）
  - `backend/THIRD_PARTY_NOTICES.md`（新增或更新）
  - `backend/internal/service/openai_ws_protocol_resolver_test.go`（protocol resolver 测试补充）
  - `backend/internal/service/admin_service.go`（校验逻辑支持 passthrough）
  - `frontend/src/utils/openaiWsMode.ts`（新增 passthrough 常量与 normalize 支持）
  - `frontend/src/utils/__tests__/openaiWsMode.spec.ts`（对应前端测试）
  - `frontend/src/components/account/EditAccountModal.vue`（UI 选项新增 passthrough）
  - `frontend/src/components/account/CreateAccountModal.vue`（UI 选项新增 passthrough）
  - `deploy/config.example.yaml`（配置示例注释补充 passthrough）

- 风险级别：中（新增模式 + 第三方实现引入）。
- 兼容性：默认不改变已有 `ctx_pool` 行为；仅账号显式配置 `passthrough` 时生效。
