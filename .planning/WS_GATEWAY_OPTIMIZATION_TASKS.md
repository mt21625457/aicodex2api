# OpenAI WebSocket v2 网关优化任务清单

**创建日期**: 2026-03-01
**主要客户端**: Codex CLI

## 已完成

- [x] **P1 消息泵模式** — 将 sendAndRelay 串行读写改为 goroutine+channel 并发泵模式，解耦上游读取和客户端写入
  - 文件: `openai_ws_forwarder.go` — `sendAndRelay` 函数
  - 新增类型: `openAIWSUpstreamPumpEvent`，缓冲 channel (16 events)
  - 排水模式通过 `time.AfterFunc` + `pumpCancel` 实现

## 待实施

### P0 — 连接生命周期管理（60分钟上限）

- [ ] OpenAI WS 连接有 60 分钟硬性限制，到期会返回 `websocket_connection_limit_reached`
- [ ] 在连接接近 60 分钟时主动创建新连接替换，避免请求中断
- [ ] 在 `openAIWSIngressContext` 中记录连接创建时间，sweeper 中检测到期连接
- 文件: `openai_ws_ingress_context_pool.go`
- 预期收益: 避免上游断连导致请求失败

### P1 — Acquire 热路径移除 cleanupExpiredLocked

- [ ] 当前 `Acquire` 在持锁期间调用 `cleanupAccountExpiredLocked`，增加锁持有时间
- [ ] 将过期清理完全交给后台 sweeper goroutine
- [ ] 减少 `ap.mu` 锁竞争
- 文件: `openai_ws_ingress_context_pool.go` 行 379
- 预期收益: 减少锁持有时间 ~30%

### P1 — Codex 客户端快速路径

- [ ] 通过 header `x-codex-client-version` 识别 Codex CLI 客户端
- [ ] 跳过不必要的 `normalizeOpenAIWSIngressPayloadBeforeSend` 逻辑
- [ ] Codex CLI 消息格式固定，无需通用 normalizer
- 文件: `openai_ws_ingress_normalizer.go`, `openai_ws_forwarder.go`
- 预期收益: 降低延迟和 CPU 开销

### P2 — 简化迁移评分模型

- [ ] 当前 `pickMigrationCandidateLocked` 有 7+ 个硬编码权重因子
- [ ] 简化为 3 个核心因子: 健康度、空闲时间、连接质量
- [ ] 提高可观测性（评分因子可日志输出）
- 文件: `openai_ws_ingress_context_pool.go`
- 预期收益: 提高可维护性和可调优性

### P2 — 动态缩容池大小

- [ ] Codex 场景下用户通常只有 1-2 个活跃会话，大部分 context 槽位空闲
- [ ] 按需增长：初始容量 1，每次 L1 新建时增长
- [ ] 空闲超时后自动缩减
- 文件: `openai_ws_ingress_context_pool.go`
- 预期收益: Codex 场景下减少内存 50%+

### P3 — 后台主动 Ping 检测

- [ ] 当前预热是惰性的（turn 开始时才 Ping）
- [ ] 后台定期对空闲 context 发送 Ping，及时剔除死连接
- [ ] Release 后延迟 5s Ping，提前发现问题
- 文件: `openai_ws_ingress_context_pool.go`
- 预期收益: 减少请求时的预热延迟

### P3 — 拆分 forwarder.go

- [ ] 当前 `openai_ws_forwarder.go` 超过 5400 行
- [ ] 拆分为: `openai_ws_turn.go`、`openai_ws_relay.go`、`openai_ws_recovery.go`
- 预期收益: 提高可维护性
