# OpenAI WS v2 `ctx_pool` 模式详细设计

## 1. 目标与边界

### 1.1 目标
- 为 **Codex CLI 入站 + OpenAI Responses WS v2** 引入新模式 `ctx_pool`。
- 粒度为：**每个 Codex CLI 客户端 WS 连接绑定一个 context**。
- context 内维护上游 WS 连接，异常时在 context 内重建，不走通用连接池。
- 客户端断开后 context 归还账号 context 池，后续同会话优先复用。
- 账号粘连严格生效：context 不跨账号复用。

### 1.2 非目标
- 不改变 `off/shared/dedicated` 现有行为。
- 不改变 HTTP/SSE 路由行为。
- 不替换现有 `response_id -> account_id`、`session_hash -> account_id` 粘连机制（继续复用）。

### 1.3 生效条件（硬门禁）
仅当以下条件同时满足时进入 `ctx_pool` 分支：
1. `transport == responses_websockets_v2`
2. `mode_router_v2_enabled == true`
3. `account.ResolveOpenAIResponsesWebSocketV2Mode(...) == "ctx_pool"`
4. 当前请求识别为 Codex CLI（沿用现有 `IsCodexCLIRequest` + force 逻辑）

否则统一走现有分支（shared/dedicated/off 或 HTTP fallback）。

---

## 2. 核心概念

## 2.1 Ingress Context
context 是一个“会话执行壳”，封装：
- 该会话的账号粘连身份（group/account/session）
- 该会话当前绑定的客户端 WS owner
- 该会话的上游 WS 连接与可恢复状态

建议结构（概念字段）：

```go
type OpenAIWSIngressContext struct {
    ID                string

    GroupID           int64
    AccountID         int64
    SessionHash       string

    OwnerClientConnID string
    OwnerLeaseAt      time.Time

    Upstream          openAIWSClientConn
    UpstreamConnID    string
    UpstreamState     string // disconnected|connected|broken

    LastResponseID    string
    LastTurnState     string
    LastPromptCacheKey string

    CreatedAt         time.Time
    LastUsedAt        time.Time
    ExpiresAt         time.Time

    RebuildCount      int
    LastErrClass      string
}
```

关键约束：
- `OwnerClientConnID` 同时只能有一个（单 owner）。
- `AccountID` 固定，不允许 context 在不同账号之间迁移。
- 上游连接状态和元数据仅在此 context 内维护。

## 2.2 账号级 Context Pool
按账号维护独立池：

```go
type OpenAIWSAccountContextPool struct {
    AccountID int64
    Capacity  int // == account.concurrency (硬约束)

    ByID      map[string]*OpenAIWSIngressContext
    BySession map[string]string // sessionHash -> contextID
}
```

全局管理器：
- 一级 key：`accountID`
- 二级 key：`sessionHash`
- 另带 `groupID` 校验，避免跨 group 误命中

**容量规则（硬约束）**：
- `capacity = account.concurrency`（与账号并发数完全一致）
- 例如账号并发 = 20，则该账号 context 池容量 = 20
- 超容量时只可回收“空闲且过期”context；若无可回收，返回 busy。

---

## 3. 账号粘连设计

## 3.1 粘连分层
保留双层粘连：
1. **账号粘连层（现有）**：`session_hash -> account_id`
2. **context 粘连层（新增）**：`(group_id, account_id, session_hash) -> context`

先账号，后 context：
- 先通过现有调度/粘连得到 `account`。
- 再在该账号池里查/建 context。

## 3.2 防漂移规则
- 同 `session_hash` 若命中不同 `account_id` 的 context，**禁止复用**。
- 若调度结果 account 与 context 记录 account 不一致，按“账号优先”处理：
  - 仅使用当前 account 对应池；
  - 记录 `account_sticky_conflict` 日志；
  - 不跨账号迁移 context。

## 3.3 续链优先级
在 context 内续链优先级：
1. 当前 context 里的上游连接（同 owner）
2. 当前 context 内重建上游连接
3. 仍失败再按既有策略返回客户端（包括 policy violation）

不从普通 WS 连接池兜底抢连（ctx_pool 模式下禁用）。

---

## 4. 生命周期与状态机

## 4.1 Context 状态
- `idle`：无 owner，可被复用
- `leased`：被某客户端 WS 独占
- `expired`：超过 TTL，待回收
- `evicted`：被清理

## 4.2 Upstream 子状态
- `disconnected`：尚未建立或已关闭
- `connected`：可收发
- `broken`：已判定不可用，等待重建

## 4.3 关键状态迁移
1. 客户端接入：`idle -> leased`
2. 首 turn 建连：`disconnected -> connected`
3. 读写失败：`connected -> broken -> disconnected -> connected(重建成功)`
4. 客户端断开：`leased -> idle`
5. TTL 到期：`idle -> expired -> evicted`

---

## 5. 请求处理时序

## 5.1 首包阶段
1. handler 读首包并选账号（沿用现有逻辑）。
2. 判断是否命中 `ctx_pool` 生效条件。
3. 若命中：
   - 用 `(groupID, accountID, sessionHash)` Acquire context（带 owner 绑定）。
   - context 中无 upstream 则建连。
4. 将首包发送到 context.upstream，开始 relay。

## 5.2 多 turn 阶段
- 同一客户端 WS 连接内，所有 turn 复用同一 context。
- `previous_response_id`、`prompt_cache_key`、turn state 都更新到 context 元数据。
- 若上游失败，在 context 内重建并重放当前 turn（遵守既有恢复边界）。

## 5.3 客户端断开阶段
- 释放 owner（CAS 清空 `OwnerClientConnID`）。
- 不立即销毁 context；进入 idle，等待同 session 后续复用。
- 若断开时处于不可恢复态，可标记 upstream broken 并关闭，保留 context 壳。

---

## 6. 并发与一致性

## 6.1 锁策略
- 全局：按账号分段（shard）锁，避免大锁。
- 账号池内：单账号 mutex 保护 map 结构。
- context owner：CAS + 短临界区，保证单 owner。

## 6.2 并发上限
- 每账号 context 总数不超过 `account.concurrency`。
- 同一 context 仅允许一个 owner；owner 冲突立即返回 `try again later`。

## 6.3 一致性原则
- 任何时刻 owner 与 upstream 操作必须同 context 线性化。
- 上游重建只在 owner 持有期间发生，避免幽灵重建。

---

## 7. 故障处理

## 7.1 上游连接错误
- classify 后进入：
  - 可恢复：context 内重建一次并重试当前 turn
  - 不可恢复：回传错误并标记 broken

## 7.2 continuation unavailable
- 在 `ctx_pool` 模式下，优先解释为“该 context 上游已丢失连续性”。
- 执行 context 内重建；若仍失败，沿用现有关闭语义（1008 policy violation）。

## 7.3 首包/压缩问题
- 仍沿用已改的 `CompressionNoContextTakeover` ingress 策略。
- `ctx_pool` 不改变首包读入、JSON 校验和 read timeout 逻辑。

---

## 8. 配置设计

## 8.1 模式值
- 新增模式值：`ctx_pool`
- 账号字段示例：
  - `openai_apikey_responses_websockets_v2_mode: "ctx_pool"`
  - `openai_oauth_responses_websockets_v2_mode: "ctx_pool"`

## 8.2 建议新增参数（可选）
- `gateway.openai_ws.ctx_pool_idle_ttl_seconds`（默认 600）
- `gateway.openai_ws.ctx_pool_sweep_interval_seconds`（默认 30）
- `gateway.openai_ws.ctx_pool_rebuild_max_per_turn`（默认 1）
- `gateway.openai_ws.ctx_pool_owner_stale_seconds`（默认 120）

这些参数仅对 `ctx_pool` 模式生效，不影响其他模式。

---

## 9. 可观测性

## 9.1 日志字段
- `openai_ws_ctx_pool_mode` bool
- `openai_ws_ctx_id`（截断）
- `openai_ws_ctx_key`（hash 后截断）
- `openai_ws_ctx_reused` bool
- `openai_ws_ctx_rebuild` bool
- `openai_ws_ctx_account_sticky_hit` bool
- `openai_ws_ctx_acquire_reason` enum（new/reuse/busy/conflict）

## 9.2 指标
- `openai_ws_ctx_pool_acquire_total{result=...}`
- `openai_ws_ctx_pool_contexts{account_id=...}`
- `openai_ws_ctx_pool_rebuild_total{reason=...}`
- `openai_ws_ctx_pool_evict_total{reason=ttl|capacity|broken}`

---

## 10. 测试矩阵

## 10.1 功能正确性
1. `ctx_pool` 模式仅 WSv2+Codex CLI 生效。
2. 单连接多 turn 复用同一 context 与同一 upstream。
3. 断开后回池；同 session 重连复用同 context。

## 10.2 粘连正确性
1. 同 session 不跨 account 复用 context。
2. account 粘连冲突时记录日志并拒绝跨账号复用。

## 10.3 稳定性
1. upstream read/write fail -> context 内重建成功。
2. 重建失败 -> 返回既有错误语义。
3. capacity 打满时返回 busy，不出现死锁或泄漏。

## 10.4 回归
- shared/dedicated/off 全回归通过。
- HTTP/SSE 路径无行为变化。

---

## 11. 发布与回滚

## 11.1 发布顺序
1. 代码上线但默认不启用（仍 dedicated）。
2. 单账号灰度 `mode=ctx_pool`。
3. 观察 1008 continuation unavailable、首包失败率、重建率。
4. 分批扩大。

## 11.2 回滚
- 账号模式切回 `dedicated/shared` 即刻退出 ctx_pool 分支。
- 如需全局兜底：`force_http=true`。

---

## 12. 实现前需确认的决策

1. context 回池 TTL：建议 10 分钟，是否接受？
2. context 容量固定等于 `account.concurrency`（已确认，不提供系数配置）。
3. context owner stale 回收是否启用（防异常断链未释放）？
4. `ctx_pool` 是否仅给 API Key 账号开放，还是 OAuth 同步开放？



---

## 13. 自适应粘连强度（Adaptive Stickiness Strength）

为避免“全程硬粘连导致不可恢复”或“过度漂移导致续链断裂”，`ctx_pool` 采用动态粘连强度。

## 13.1 双评分模型
每个 turn 计算两个分值：

1. **续链风险分（ContinuationRisk, 0-10）**
- `+4`: `previous_response_id` 非空
- `+3`: `input` 含 `function_call_output` 或 `item_reference`
- `+2`: `store=false`
- `+1`: 连续 turn 间隔 < 15s（强会话态）

2. **账号健康风险分（AccountHealthRisk, 0-10）**
- `+3`: 最近 60s WS 失败率 > 15%
- `+3`: 最近 60s `policy_violation/continuation_unavailable` 占比 > 10%
- `+2`: 最近 60s p95 首 token 延迟显著恶化（超过基线 2x）
- `+2`: 账号并发饱和（可用 context 为 0）

## 13.2 粘连强度档位

- **L3 HARD（强粘连）**
  - 条件：`ContinuationRisk >= 6`
  - 行为：必须保持 `(account + context)`，禁止迁移，仅允许 context 内重建。

- **L2 PREFER（偏好粘连）**
  - 条件：`ContinuationRisk in [3,5]`
  - 行为：优先同 account 同 context；必要时允许同 account 内 context 置换，不跨账号。

- **L1 SOFT（软粘连）**
  - 条件：`ContinuationRisk <= 2`
  - 行为：以账号粘连为默认，但允许在健康恶化时进入“可迁移候选”。

## 13.3 动态切换原则
- 每 turn 重新评估，允许 L1/L2/L3 升降。
- 仅允许“逐级降粘”防抖：`L3 -> L2 -> L1`，每级最短驻留时间 2 turn。
- 一旦命中 function_call_output，立即提升到 `L3`，至少保持 2 turn。

---

## 14. 智能迁移（Intelligent Migration）

迁移目标：在不破坏续链语义的前提下，把会话从劣化账号迁移到更健康账号。

## 14.1 可迁移前提（全部满足）
1. 当前粘连强度为 `L1 SOFT`。
2. 当前 turn 无 `previous_response_id`，或已构造为 full-create（可重放完整 input）。
3. 无未闭合 `function_call_output` 链路。
4. 当前 context 不是 owner 冲突状态。
5. 目标账号由调度器评估为明显更优（健康分差 >= 阈值）。

## 14.2 迁移流程（两阶段）

### Phase A: 预热与校验
1. 选 target account（同 group，模型可用，限流可用）。
2. 在 target account 池创建/获取 `target context`。
3. 建立 target 上游 WS，并做轻量探测（ping/首包握手头校验）。
4. 复制可迁移元数据（turn_state、prompt_cache_key、会话标签，不复制失效 conn_id）。

### Phase B: 原子切换
1. 冻结 source context 的新 turn 写入（短时间门闩）。
2. 将 `session_hash -> account_id` 粘连更新为 target（短 TTL 试运行，例如 5 分钟）。
3. owner 切换到 target context，source 标记 draining。
4. 下一 turn 从 target context 发起。

若任一步失败：
- 回滚到 source context；
- 记录 `migration_failed`，进入短时间冷却（例如 60s 禁止再次迁移）。

## 14.3 迁移后稳定期
- 迁移成功后 3 turn 内锁定 `L2`（防抖回跳）。
- 若 3 turn 内失败率上升，允许自动回切 source（若 source 仍健康可用）。

---

## 15. 分层调度（Hierarchical Scheduling）

`ctx_pool` 调度不是单层“找连接”，而是三层逐级决策：

## 15.1 L0: Context 层调度（会话内）
输入：`group_id + account_id + session_hash + stickiness_level`

优先级：
1. owner 持有 context（当前连接）
2. 同 account + 同 session 的 idle context
3. 同 account 新建 context（若 capacity 允许）
4. 同 account 回收过期 context 后重建

## 15.2 L1: 账号层调度（会话外）
当 L0 无可用上下文或触发迁移候选时：
- 在当前 sticky account 与候选 account 之间比较健康分：
  - 失败率
  - p95 首 token
  - 可用 context 比例
  - 当前限流压力

输出：`stay | migrate_candidate`。

## 15.3 L2: 组层调度（全局候选）
当需要迁移时，复用现有账号调度器 topK 候选，附加 `ctx_pool` 约束：
- 仅选支持 WSv2 + ctx_pool 的账号
- 仅选并发仍有余量账号
- 模型映射可用

最终输出 target account。

## 15.4 调度结果缓存
- 将最近一次 `session_hash -> selected_account` 决策缓存短 TTL（例如 30s），减少抖动。
- 强续链（L3）禁用该短缓存覆盖，始终强制 stay。

---

## 16. 关键策略矩阵

| 粘连强度 | 账号健康 | previous_response_id | 行为 |
|---|---|---|---|
| L3 HARD | 任意 | 有 | 强制 stay，context 内重建，禁止迁移 |
| L2 PREFER | 健康 | 可有可无 | stay；同账号内可置换 context |
| L2 PREFER | 恶化 | 无 | stay 优先，必要时准备迁移但不立即切 |
| L1 SOFT | 健康 | 无 | stay（低成本复用） |
| L1 SOFT | 恶化 | 无 | 触发智能迁移流程 |

---

## 17. 新增可观测性（针对三能力）

新增日志/指标维度：
- `openai_ws_stickiness_level`：L1/L2/L3
- `openai_ws_continuation_risk_score`
- `openai_ws_account_health_risk_score`
- `openai_ws_migration_candidate` / `openai_ws_migration_committed`
- `openai_ws_scheduler_layer`：L0/L1/L2
- `openai_ws_scheduler_decision_reason`

用于回答三类问题：
1. 为什么现在是强粘连？
2. 为什么发生/没发生迁移？
3. 调度卡在 context 层、账号层还是组层？

---

## 18. 实施顺序（建议）

1. 先落 L0（context 层）+ L3/L2 基础粘连，保证正确性。
2. 再加评分与 L1（软粘连）。
3. 最后加智能迁移（双阶段切换）与 L2 组层候选。
4. 全程灰度，按账号逐步打开迁移能力（feature flag）。
