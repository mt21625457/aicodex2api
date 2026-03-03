## M0. Proposal Gate

- [ ] M0.1 确认 `passthrough` 模式职责边界：仅认证替换 + 计费解析。
- [ ] M0.2 确认 `ctx_pool` 旧语义保持不变（新增模式并行）。
- [ ] M0.3 确认 Caddy 代码引入范围与许可证处理方式。

## M1. Config and Mode Resolution

- [ ] M1.1 扩展 `gateway.openai_ws.ingress_mode_default` 支持 `passthrough`。
- [ ] M1.2 扩展账号 `*_responses_websockets_v2_mode` 解析支持 `passthrough`。
- [ ] M1.3 保持旧布尔字段兼容映射（仅映射到 `off|ctx_pool`）。
- [ ] M1.4 补充 `config.Validate()` 与默认值测试。

## M1.5. Protocol Resolver Adaptation

- [ ] M1.5.1 在 `openai_ws_protocol_resolver.go` 新增 `passthrough` 路由分支。
- [ ] M1.5.2 补充 protocol resolver 测试（`openai_ws_protocol_resolver_test.go`）。

## M2. Passthrough Forwarder Path

- [ ] M2.1 在 `ProxyResponsesWebSocketFromClient` 增加 `mode=passthrough` 分支。
- [ ] M2.2 新增 `proxyResponsesWebSocketPassthroughV2` 实现（独立文件）。
- [ ] M2.3 接入双向 relay（client->upstream / upstream->client）并发模型。
- [ ] M2.4 支持 stream timeout 与统一关闭收敛。
- [ ] M2.5 在 handler `ResponsesWebSocket` 中新增 passthrough 早期分流路径（跳过 Turn hooks 构建，独立获取/释放并发槽位）。

## M3. Caddy Code Integration

- [ ] M3.1 引入并适配 Caddy streaming 核心代码（最小变更）。
- [ ] M3.2 在代码中标注来源文件与 commit pin。
- [ ] M3.3 新增/更新第三方许可证声明文件（NOTICE）。

## M4. Semantic Zero-Mutation Enforcement

- [ ] M4.1 passthrough 路径禁用 payload mutation（type/model/metadata）。
- [ ] M4.2 passthrough 路径禁用 `previous_response_id` 注入/删除/对齐。
- [ ] M4.3 passthrough 路径禁用 proactive reject 与 recovery/replay。
- [ ] M4.4 增加 `semantic_mutation_total` 保护指标（期望始终为 0）。

## M5. Billing and Auth

- [ ] M5.1 握手阶段仅替换上游认证头（Authorization）。
- [ ] M5.2 上游下行旁路提取 usage，转发字节保持不变。
- [ ] M5.3 usage 解析失败仅告警，不影响透传。
- [ ] M5.4 对齐现有 `RecordUsage` 异步入库流程。

## M6. Tests

- [ ] M6.1 新增 passthrough 单元测试：消息字节一致性（含 function_call_output）。
- [ ] M6.2 新增测试：passthrough 不触发 `previous_response_id` 修复逻辑。
- [ ] M6.3 新增测试：passthrough 不触发 recovery/replay。
- [ ] M6.4 新增集成测试：上游 error event 原样透传。
- [ ] M6.5 回归测试：`ctx_pool` 现有行为不变。

## M7. Rollout

- [ ] M7.1 先灰度少量账号切 `passthrough`。
- [ ] M7.2 观察 `semantic_mutation_total=0` 与 relay error 指标。
- [ ] M7.3 指标异常按账号回切 `ctx_pool`。
- [ ] M7.4 灰度稳定后扩大到全量目标账号。
