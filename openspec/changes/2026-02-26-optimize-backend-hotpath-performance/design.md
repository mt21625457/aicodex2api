## Design Overview

本设计采用统一原则：

- **先降复杂度，再抠常数项**：先清除 N+1 / 循环查库，再优化 JSON 热路径。
- **语义不变，路径重构**：不改业务规则，只改执行方式。
- **每项可观测、可回滚**：所有优化都配套指标与独立开关。

---

## Phase P0：查询复杂度收敛（最高优先级）

### P0-1 批量账号快照同步

现状：`BulkUpdate` 在需要同步时逐账号调用 `syncSchedulerAccountSnapshot`，触发深查询放大。  
设计：新增批量路径。

建议接口：

- `accountRepo.SyncSchedulerAccountSnapshots(ctx, ids []int64) error`
- `schedulerCache.SetAccounts(ctx, accounts []*service.Account) error`

执行策略：

1. `BulkUpdate` 一次性收集需同步账号。
2. 一次 `GetByIDs` 获取快照必要信息。
3. 一次批量写缓存。

复杂度：`O(N*deep-query)` -> `O(1~2 DB + 1 cache batch)`。

### P0-2 Outbox 批量账号事件

现状：`handleBulkAccountEvent` 逐账号调用 `handleAccountEvent`。  
设计：改为批量消费。

建议接口：

- `handleBulkAccountEvent(ctx, payload)` 内部直接 `GetByIDs`
- `rebuildByGroupAndPlatformBatch(ctx, jobs []RebuildJob)`

执行策略：

1. 批量加载账号。
2. 构建 `platform+groupID` 去重集合。
3. 批量更新 cache + 批量 rebuild。

### P0-3 混合渠道检查批量化

现状：批量账号更新中，每个账号都对每个 group 调 `ListByGroup`。  
设计：单次预加载索引。

建议接口：

- `accountRepo.ListByGroups(ctx, groupIDs []int64) (map[int64][]Account, error)`
- 或 `groupRepo.GetGroupAccountPlatforms(ctx, groupIDs []int64) (map[int64]PlatformSet, error)`

执行策略：

1. 请求级预加载 group -> platform 集合。
2. 按账号平台在内存中判冲突。
3. 仅错误回传时补查 group 名称。

### P0-4 Gemini 预检批量化

现状：候选循环内逐账号分钟查询。  
设计：请求级批量 usage 预取。

建议接口：

- `rateLimitService.PreCheckUsageBatch(ctx, accounts []Account, requestedModel string) (map[int64]bool, error)`

SQL 草案（分钟窗口）：

```sql
SELECT account_id,
       SUM(CASE WHEN ... THEN 1 ELSE 0 END) AS req_count
FROM usage_logs
WHERE created_at >= $1 AND created_at < $2
  AND account_id = ANY($3)
GROUP BY account_id;
```

执行策略：

1. 先批量聚合 usage。
2. 生成 `account_id -> pass/fail`。
3. 选择器只消费内存结果。

### P0-5 管理链路批量化

- `ListUsers`：`GetByUserIDs` 一次查询回填。
- `SyncUserGroupRates`：单事务批量 upsert/delete。
- 分组校验：`ExistsByIDs` 替代循环 `GetByID`。

---

## Phase P1：热路径 CPU/分配优化

### P1-1 SSE 单事件单次解析

现状：同事件多次 `Unmarshal/Marshal`。  
设计：事件门控 + 解析复用。

执行策略：

1. 非目标事件直接透传（不解析）。
2. 目标事件仅解析一次。
3. usage 提取复用同对象。
4. 仅在字段被改写时再序列化。

目标：单事件结构化解析次数 `<= 1`。

### P1-2 模型映射惰性缓存

现状：`GetModelMapping` 高频重建 map。  
设计：`Account` 内缓存非持久化字段。

建议字段：

- `cachedModelMapping map[string]string`
- `cachedMappingReady bool`

失效策略：

- 当 `Credentials` 被整体替换时重置缓存。

---

## Phase P2：Ops raw 查询收敛与降级

现状：同请求重复扫描 `usage_logs/ops_error_logs`。  
设计：共享 CTE + 超时降级。

执行策略：

1. 统一过滤条件与时间窗 CTE。
2. 指标尽量一次扫描产出。
3. 百分位超时则返回基础指标与 `degraded=true`。

原则：preagg 优先，raw 仅作兜底。

---

## Observability & SLO

必须新增以下埋点：

- 每请求 DB 查询总数（按接口）
- Gemini 预检 SQL 次数
- SSE 每事件解析次数
- Ops raw 查询耗时与降级次数

发布闸门（同 proposal）：

- 批量更新 DB 查询下降 >= 70%
- Gemini 预检分钟 SQL 次数 <= 1
- SSE `allocs/op` 下降 >= 25%
- Ops raw P95 下降 >= 30%

---

## Risks & Mitigations

1. 批量预加载导致内存峰值上升
- 缓解：按 group 分批加载，设置单请求最大 group/account 上限。

2. 模型映射缓存失效不及时
- 缓解：封装 `SetCredentials` 统一失效；关键路径单测覆盖。

3. Ops raw 合并后 SQL 复杂度提高
- 缓解：分层构建 CTE，保留 fallback 查询路径与超时保护。

4. SSE 优化导致协议兼容回归
- 缓解：建立事件回放回归集（message_start/message_delta/error/DONE）。

---

## Rollout Plan

1. 先启用 P0 并灰度 10%。
2. 指标稳定后启用 P1。
3. 最后启用 P2，并验证看板场景降级能力。
4. 任一阶段越界，按子域开关即时回滚。
