## M0. Proposal Gate

- [x] M0.1 确认 `passthrough` 模式职责边界：仅认证替换 + 计费解析。
- [x] M0.2 确认 `ctx_pool` 旧语义保持不变（新增模式并行）。
- [x] M0.3 确认 Caddy 代码引入范围与许可证处理方式。

## M1. Config and Mode Resolution

- [x] M1.1 扩展 `gateway.openai_ws.ingress_mode_default` 支持 `passthrough`。
- [x] M1.2 扩展账号 `*_responses_websockets_v2_mode` 解析支持 `passthrough`。
- [x] M1.3 保持旧布尔字段兼容映射（仅映射到 `off|ctx_pool`）。
- [x] M1.4 补充 `config.Validate()` 与默认值测试。

## M1.5. Protocol Resolver Adaptation

- [x] M1.5.1 在 `openai_ws_protocol_resolver.go` 新增 `passthrough` 路由分支。
- [x] M1.5.2 补充 protocol resolver 测试（`openai_ws_protocol_resolver_test.go`）。

## M1.6. Directory Layout (openai_ws_v2)

- [x] M1.6.1 新建 `backend/internal/service/openai_ws_v2/` 目录并建立 `entry.go` 入口骨架。
- [x] M1.6.2 新增 `caddy_adapter.go`，封装 Caddy streaming 适配逻辑。
- [x] M1.6.3 新增 `passthrough_relay.go`，承载 v2 passthrough 双向隧道实现。
- [x] M1.6.4 约束 `openai_ws_forwarder.go` 仅保留 mode 分流与薄适配，不新增 passthrough 细节逻辑。
- [x] M1.6.5 定义依赖注入接口边界，确保 `openai_ws_v2` 与 `internal/service` 无循环依赖。

## M2. Passthrough Forwarder Path

- [x] M2.0 **不修改** `openAIWSClientConn` 接口，仅在具体类型 `coderOpenAIWSClientConn` 上新增 `ReadFrame(ctx) → (MessageType, []byte, error)` 和 `WriteFrame(ctx, MessageType, []byte) → error` 方法，直接委托 `coderws.Conn.Read/Write`。`openai_ws_v2` 包内定义独立 `FrameConn` 接口，service 层通过适配器桥接。
- [x] M2.1 在 `ProxyResponsesWebSocketFromClient` 增加 `mode=passthrough` 分支。
- [x] M2.2 在 `openai_ws_v2/passthrough_relay.go` 实现 `proxyResponsesWebSocketPassthroughV2`。
- [x] M2.3 接入双向 relay（client->upstream / upstream->client）并发模型。
- [x] M2.4 支持 idle timeout（读空闲超时）与统一关闭收敛。
- [x] M2.5 在 handler `ResponsesWebSocket` 中新增 passthrough 早期分流路径（跳过 Turn hooks 构建，独立获取/释放并发槽位）。
- [x] M2.6 明确并实现调度闭环：`passthrough` 与 `ctx_pool` 均复用 `SelectAccountWithScheduler`，并保持 `BindStickySession` 一致策略。
- [x] M2.7 首包校验分流：`model` 非空校验在 passthrough/ctx_pool 都执行（空值本地失败且不上游）；`previous_response_id` 格式校验仅 ctx_pool 执行。
- [x] M2.8 passthrough data-plane 禁止 `OpenAIWSStateStore` 语义修复读写（`ResponseAccount/ResponseConn/ResponsePendingToolCalls/SessionTurnState/SessionLastResponseID/SessionConn`）。

## M3. Caddy Code Integration

- [x] M3.1 引入并适配 Caddy streaming 核心代码（最小变更）。
- [x] M3.2 在代码中标注来源文件与 commit pin。
- [x] M3.3 新增/更新第三方许可证声明文件（NOTICE）。

## M4. Semantic Zero-Mutation Enforcement

- [x] M4.1 passthrough 路径禁用 payload mutation（type/model/metadata）。
- [x] M4.2 passthrough 路径禁用 `previous_response_id` 注入/删除/对齐。
- [x] M4.3 passthrough 路径禁用 proactive reject 与 recovery/replay。
- [x] M4.4 增加 `semantic_mutation_total` 保护指标（期望始终为 0）。

## M5. Billing and Auth

- [x] M5.1 握手阶段仅替换上游认证头（Authorization）。
- [x] M5.2 上游下行旁路提取 usage，转发字节保持不变。
- [x] M5.3 usage 解析失败仅告警，不影响透传。
- [x] M5.4 对齐现有 `RecordUsage` 异步入库流程。

## M6. Tests

- [x] M6.1 新增 passthrough 单元测试：消息字节一致性（含 function_call_output）。
- [x] M6.2 新增测试：passthrough 不触发 `previous_response_id` 修复逻辑。
- [x] M6.3 新增测试：passthrough 不触发 recovery/replay。
- [x] M6.4 新增集成测试：上游 error event 原样透传。
- [x] M6.5 回归测试：`ctx_pool` 现有行为不变。
- [x] M6.6 新增测试：`mode=passthrough` 且首包 `model` 为空时本地失败，且不上游发起 WS dial。
- [x] M6.7 新增测试：passthrough 与 ctx_pool 在同样 `groupID/sessionHash/model` 输入下走同一调度入口（含 sticky session 回写）。
- [x] M6.8 新增测试：passthrough 仅使用 idle timeout（帧到达可续期），不采用绝对生命周期超时。
- [x] M6.9 新增测试：passthrough 路径不触发 `OpenAIWSStateStore` 相关 API 调用。
- [x] M6.10 新增静态校验：新增 passthrough 数据面实现文件仅位于 `openai_ws_v2/`，`openai_ws_forwarder.go` 不出现新数据面逻辑。

## M6.11. Frontend and Admin Adaptation

- [x] M6.11.1 在 `frontend/src/utils/openaiWsMode.ts` 新增 `OPENAI_WS_MODE_PASSTHROUGH` 常量和 normalize 支持。
- [x] M6.11.2 更新 `EditAccountModal.vue` / `CreateAccountModal.vue` 选项列表新增 passthrough。
- [x] M6.11.3 更新 `admin_service.go` 校验逻辑支持 passthrough 模式。
- [x] M6.11.4 补充前端 openaiWsMode spec 测试（`openaiWsMode.spec.ts`）。
- [x] M6.11.5 更新 `deploy/config.example.yaml` 配置注释说明 passthrough 选项。

## M7. Rollout

- [x] M7.1 先灰度少量账号切 `passthrough`。
- [x] M7.2 观察 `semantic_mutation_total=0` 与 relay error 指标。
- [x] M7.3 指标异常按账号回切 `ctx_pool`。
- [x] M7.4 灰度稳定后扩大到全量目标账号。
