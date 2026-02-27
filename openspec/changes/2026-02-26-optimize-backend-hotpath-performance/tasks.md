## M0. 提案质量门禁（已完成）

- [x] M0.1 第 1 轮复审：检查“问题是否全部覆盖 + 是否有最优优先级路径”。
- [x] M0.2 第 2 轮复审：对齐 proposal/design/tasks/spec 的验收口径。
- [x] M0.3 第 3 轮复审：补齐发布闸门、回滚边界、可观测指标。

## M1. 已完成实施（从提案问题列表移除）

- [x] M1.1 批量快照同步：`BulkUpdate` 不再逐账号深查询同步。
- [x] M1.2 outbox 批量账号事件改为批量加载与一次性分组重建。
- [x] M1.3 批量更新混合渠道检查改为单次预加载分组成员。
- [x] M1.4 用户列表倍率加载增加批量读取路径（`GetByUserIDs`）。
- [x] M1.5 用户分组倍率同步改为批量 delete + 批量 upsert。
- [x] M1.6 分组存在性校验增加批量检查路径（create/update/bulk update）。

## M2. 剩余实施（已完成）

- [x] M2.1 新增 `PreCheckUsageBatch`，移除 Gemini 候选循环逐账号分钟查库。
- [x] M2.2 SSE 事件门控 + usage 单次解析复用。
- [x] M2.3 `Account` 增加 `model_mapping` 惰性缓存与失效机制。
- [x] M2.4 Ops raw 查询共享 CTE + 超时降级返回。

## M3. 验证、灰度与回滚

- [ ] M3.1 新增埋点：Gemini 预检 SQL 次数、SSE 事件解析次数、Ops raw 降级次数。
- [ ] M3.2 在 staging 输出“优化前后”压测报告（P50/P95/P99、allocs/op、B/op、慢 SQL）。
- [ ] M3.3 按开关分阶段灰度发布（R1 -> R2）。
- [ ] M3.4 任一闸门未达标时执行子域级回滚并记录复盘。
