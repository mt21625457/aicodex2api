## ADDED Requirements

### Requirement: OpenAI WS v2 MUST support passthrough mode
系统 MUST 支持 `openai ws mode v2` 的 `passthrough` 模式，并与现有 `ctx_pool` 路径并行存在。

#### Scenario: account mode passthrough
- **WHEN** 账号有效模式解析为 `passthrough`
- **THEN** 系统 MUST 进入 passthrough 转发路径
- **AND** MUST NOT 进入 `ctx_pool` 语义增强路径

### Requirement: OpenAI WS v2 passthrough implementation MUST be isolated under openai_ws_v2 directory
系统 MUST 将 OpenAI WS v2 passthrough 新增实现收敛到专用目录，避免继续堆积在历史 forwarder 文件中。

#### Scenario: add new passthrough implementation files
- **WHEN** 开发新增 OpenAI WS v2 passthrough 相关实现
- **THEN** 新代码 MUST 位于 `backend/internal/service/openai_ws_v2/`
- **AND** `backend/internal/service/openai_ws_forwarder.go` MUST NOT 新增 passthrough 数据面细节逻辑（仅保留入口分流和薄适配）
- **AND** 本次变更 MAY 保留历史 `ctx_pool` 逻辑在原目录，不强制迁移

### Requirement: Passthrough frame support MUST be backward-compatible with existing WS client contract
系统 MUST 在新增帧级转发能力时保持现有 `ctx_pool` 路径契约稳定，避免扩大通用接口导致大范围回归。

#### Scenario: adding frame-level methods for passthrough
- **WHEN** 实现 passthrough 所需的帧级读写能力
- **THEN** 系统 MUST 保持现有 `openAIWSClientConn` 主接口契约不变
- **AND** MAY 在具体连接实现或 v2 专用适配层新增帧级方法
- **AND** MUST NOT 影响现有 `ctx_pool` 路径测试与 mock 用法

### Requirement: Passthrough mode MUST preserve payload semantics
系统 MUST 在 passthrough 模式下保持请求与响应语义不变。

#### Scenario: function_call_output without previous_response_id
- **WHEN** 客户端发送缺失 `previous_response_id` 的 `function_call_output`
- **THEN** 网关 MUST 原样透传该请求
- **AND** MUST NOT 注入/删除/改写 `previous_response_id`

#### Scenario: model/type/client_metadata fields
- **WHEN** 客户端发送任意合法 `response.create` 负载
- **THEN** 网关 MUST NOT 修改 `type/model/input/tools/client_metadata`

### Requirement: Passthrough mode MUST disable semantic recovery logic
系统 MUST 在 passthrough 模式下禁用语义恢复能力。

#### Scenario: upstream returns previous_response_not_found or tool_output_not_found
- **WHEN** 上游返回上述错误事件
- **THEN** 网关 MUST 原样转发错误
- **AND** MUST NOT 执行 replay/retry/drop/inject 修复流程

### Requirement: Passthrough mode MUST only apply auth replacement and billing extraction
系统 MUST 将网关处理限制在认证替换与计费提取。

#### Scenario: handshake to upstream
- **WHEN** 建立上游 WS 连接
- **THEN** 网关 MUST 替换上游鉴权头为账号 token
- **AND** MUST NOT 对业务 payload 做语义修改

#### Scenario: usage side-channel parsing
- **WHEN** 上游下行事件包含 usage 字段
- **THEN** 系统 SHALL 旁路提取 usage 用于计费
- **AND** 提取失败 MUST NOT 影响透传链路

### Requirement: Passthrough relay MUST use Caddy-style bidirectional tunnel model
系统 MUST 采用 Caddy 风格的双向隧道模型进行 WS 转发。

#### Scenario: bidirectional forwarding lifecycle
- **WHEN** 下游与上游连接建立完成
- **THEN** 系统 MUST 启动双向并发复制（client->upstream, upstream->client）
- **AND** 任一方向终止时 MUST 触发连接收敛关闭
- **AND** 支持 idle timeout（读空闲超时）触发的强制关闭能力

### Requirement: Third-party code adoption MUST be license compliant
系统 MUST 在引入 Caddy 代码时满足许可证与来源可追溯要求。

#### Scenario: code import
- **WHEN** 仓库引入 Caddy 代码片段
- **THEN** MUST 保留版权/许可证声明
- **AND** MUST 在第三方声明文件记录来源文件与 commit

### Requirement: Passthrough mode MUST reject invalid first packet when model is empty
系统 MUST 在 passthrough 模式下对首包执行最小前置校验：`model` 为空时本地失败，不透传到上游。

#### Scenario: empty model field in passthrough
- **WHEN** mode=passthrough 且客户端首包 model 字段为空
- **THEN** 系统 MUST 本地拒绝连接并返回可读错误
- **AND** MUST NOT 建立上游 WS 连接

### Requirement: Passthrough mode MUST keep previous_response_id untouched
系统 MUST 在 passthrough 模式下保留 `previous_response_id` 原始值，不做本地格式修复。

#### Scenario: previous_response_id provided by client
- **WHEN** 客户端携带任意 `previous_response_id` 发起 `response.create`
- **THEN** 网关 MUST 原样转发该字段
- **AND** MUST NOT 注入/删除/改写该字段

### Requirement: Passthrough mode MUST manage concurrency at connection level
系统 MUST 在 passthrough 模式下以连接级别管理并发槽位，而非 Turn 级别。

#### Scenario: concurrency slot acquisition and release
- **WHEN** 系统进入 passthrough 转发路径
- **THEN** MUST 在连接建立时获取用户并发槽位 + 账号并发槽位
- **AND** MUST 在连接断开时释放所有槽位（通过 `defer`）
- **AND** MUST NOT 使用 Turn 级别的槽位管理

#### Scenario: concurrency slot exhausted
- **WHEN** 并发槽位已满
- **THEN** MUST 拒绝连接并返回 WS close（status 1013 Try Again Later）
- **AND** MUST NOT 排队等待

### Requirement: Passthrough mode MUST use full-connection tunnel (no Turn cycling)
系统 MUST 在 passthrough 模式下建立全程双向隧道，不按 Turn 切分请求/响应周期。

#### Scenario: full tunnel lifecycle
- **WHEN** mode=passthrough
- **THEN** 系统 MUST 建立全程双向隧道直到一方断开
- **AND** MUST NOT 按 Turn 切分请求/响应周期
- **AND** MUST NOT 调用 `hooks.BeforeTurn`
- **AND** MAY 在连接结束时调用一次 `hooks.AfterTurn`（仅用于计费/调度上报）

### Requirement: Passthrough mode MUST NOT reconnect on upstream disconnect
系统 MUST 在上游 WS 连接断开时直接终止客户端连接，不做重连。

#### Scenario: upstream connection lost
- **WHEN** 上游 WS 连接断开
- **THEN** 系统 MUST 终止客户端连接（发送 close frame 或直接关闭连接）
- **AND** MUST NOT 尝试重连或重试
- **AND** MUST 触发 `defer` 函数完成 usage 写入和槽位释放

### Requirement: Passthrough mode MUST NOT use ingressCtxPool
系统 MUST 在 passthrough 模式下直接 dial 上游，不经过连接池。

#### Scenario: upstream connection establishment
- **WHEN** mode=passthrough
- **THEN** 系统 MUST 直接通过 `openAIWSClientDialer` 建立上游连接
- **AND** MUST NOT 使用 `ingressCtxPool`

### Requirement: Passthrough mode MUST cooperate with existing scheduler path
系统 MUST 与现有调度链路闭环配合，保证账号选择与粘性策略一致。

#### Scenario: account scheduling before passthrough relay
- **WHEN** mode=passthrough 且首包通过最小校验
- **THEN** 系统 MUST 复用 `SelectAccountWithScheduler` 进行账号选择
- **AND** 可按现有策略执行 `BindStickySession`
- **AND** MUST 保持与 `ctx_pool` 路径一致的调度输入（`groupID/sessionHash/model`）

### Requirement: Passthrough mode MUST NOT use WS semantic-recovery state store
系统 MUST 禁用 WS 语义修复相关 state store 读写，但不影响调度路径使用的会话粘性缓存。

#### Scenario: passthrough data-plane processing
- **WHEN** mode=passthrough 进入双向隧道转发
- **THEN** 系统 MUST NOT 调用 `OpenAIWSStateStore` 的 `ResponseAccount/ResponseConn/ResponsePendingToolCalls/SessionTurnState/SessionLastResponseID/SessionConn` 读写
- **AND** MUST NOT 触发基于上述状态的 replay/retry/inject 修复

### Requirement: Passthrough mode MUST align with OpenAI WebSocket Mode connection semantics
系统 MUST 与 OpenAI WebSocket Mode 的连接语义保持一致。

#### Scenario: sequential responses on one socket
- **WHEN** 客户端在同一连接发送多个 `response.create`
- **THEN** 网关 MUST NOT 引入本地多路复用、并发重排或合并执行
- **AND** 对并发/顺序违规的最终裁决 MUST 交由上游协议语义处理

#### Scenario: 60-minute connection lifecycle
- **WHEN** 上游因连接生命周期上限关闭（例如 60 分钟限制）
- **THEN** 网关 MUST 将上游 close/error 原样下发客户端
- **AND** MUST NOT 自动重连

### Requirement: Ops endpoint MUST expose passthrough rollout metrics
系统 MUST 提供可用于灰度发布门禁的 passthrough 指标读取接口。

#### Scenario: fetch passthrough metrics for rollout guard
- **WHEN** 管理员访问 `GET /api/v1/admin/ops/openai-ws-v2/passthrough-metrics`
- **THEN** 响应 MUST 包含 `semantic_mutation_total`
- **AND** 响应 MUST 包含 `usage_parse_failure_total`
