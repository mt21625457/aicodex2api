## M0. Triple Review Gate

- [ ] M0.1 第 1 轮审核：确认“新增不改旧实现”边界。
- [ ] M0.2 第 2 轮审核：确认协议对称约束作用域与失败语义。
- [ ] M0.3 第 3 轮审核：确认“账号并发数=池上限”规则与容量口径。

## M1. Router Isolation

- [ ] M1.1 新增 `gateway.openai_ws.mode_router_v2_enabled`（默认 `false`）。
- [ ] M1.2 在入口增加 legacy/v2 分支，不删除 legacy 逻辑。
- [ ] M1.3 增加回归测试：v2 关闭时行为与当前基线一致。

## M2. Tri-Mode Config

- [ ] M2.1 新增 `gateway.openai_ws.ingress_mode_default`，校验 `off|shared|dedicated`。
- [ ] M2.2 账号 extra 新增 `*_mode` 字段读取（oauth/apikey）。
- [ ] M2.3 旧字段映射兼容（`*_enabled` 与兼容旧键）。

## M3. Protocol Symmetry (V2 Only)

- [ ] M3.1 v2 + WS 入站仅允许 `ws->ws`。
- [ ] M3.2 v2 + HTTP 入站仅允许 `http->http`。
- [ ] M3.3 禁止 `ws->http` 与 `http->ws` 并返回明确错误。

## M4. Account Concurrency as Pool Max

- [ ] M4.1 在 v2 路径将 `account.concurrency` 作为账号池上限。
- [ ] M4.2 `account.concurrency<=0` 的拒绝语义与日志。
- [ ] M4.3 dedicated/shared 两种模式均应用该上限。
- [ ] M4.4 新增单测覆盖并发上限命中与拒绝。

## M5. Dedicated Stability

- [ ] M5.1 dedicated 首轮独占建连。
- [ ] M5.2 dedicated 会话内连接硬亲和。
- [ ] M5.3 `store=false` 前置治理 `previous_response_id`。
- [ ] M5.4 连接中断 input replay 单次恢复。
- [ ] M5.5 会话结束连接不可复用。

## M6. Frontend/Admin

- [ ] M6.1 账号 WS mode 从布尔改为三态选择器。
- [ ] M6.2 增加“账号并发数=池上限”说明文案。
- [ ] M6.3 保持旧字段读取兼容，新字段优先写入。

## M7. Observability and Rollout

- [ ] M7.1 增加 `router_version/ws_mode/protocol_path` 关键日志。
- [ ] M7.2 增加 symmetry reject 与 pool limit hit 指标。
- [ ] M7.3 先开 shared 灰度，再开 dedicated 灰度。
- [ ] M7.4 指标异常时仅关 `mode_router_v2_enabled` 回滚。
