## Why

本提案最初覆盖 10 个性能问题。当前已完成全部实施与编译/定向测试验证，提案中的“待修复问题”已清空。

## 已修复并移出提案的问题

1. 批量账号更新后逐账号同步快照（N+1）。
2. outbox 批量账号事件逐账号处理。
3. 批量更新中的混合渠道检查重复扫描分组成员。
4. 用户列表加载用户倍率 N+1。
5. 用户分组倍率同步逐条 delete/upsert。
6. 分组存在性逐条 `GetByID` 校验（create/update/bulk update 路径）。
7. Gemini 候选筛选内逐账号分钟预检查库。
8. Ops Dashboard raw 路径串行多条重聚合 SQL。
9. SSE 每事件重复 JSON 解码/编码与二次 usage 解析。
10. 模型映射每次调用都重建 map。

## 当前待修复问题（提案范围）

无（已全部移出）。

## Problem-to-Solution Matrix（已完成）

1. Gemini 预检查询放大
- 方案：请求级批量 usage 预取（分钟+日窗口），候选循环只消费内存结果。

2. Ops raw 串行重聚合
- 方案：preagg 优先；raw 共享 CTE + 超时降级。

3. SSE 热路径重复解析
- 方案：事件门控 + 单事件单次解析 + 无改写事件直透。

4. 模型映射重复构建
- 方案：`Account` 内惰性缓存 + 凭证更新失效。

## Optimal Strategy（已执行路径）

### Phase R1（已完成）
- Gemini 预检批量化（问题 1）
- SSE 单次解析（问题 3）

原因：这两项直接影响实时请求链路，收益最高。

### Phase R2（已完成）
- 模型映射惰性缓存（问题 4）
- Ops raw 查询收敛与降级（问题 2）

原因：前者降低调度热点分配，后者优化看板与峰值稳定性。

## What Changes（已实施）

### A. Gemini 配额预检
- 新增批量预检接口：单请求一次聚合 usage，替换候选循环逐账号查库。

### B. 网关 SSE 热路径
- 单事件单次解析。
- usage 提取复用解析对象。
- 无改写事件直接透传。

### C. 模型映射缓存
- `Account` 增加 `model_mapping` 惰性缓存字段与失效逻辑。

### D. Ops raw 查询
- 共享 CTE 收敛扫描。
- 增加超时降级，保持 preagg 优先。

## Non-goals

- 不改变业务语义（调度、配额、协议）。
- 不引入强依赖数据库扩展。

## Capabilities

### Added Capabilities

- `backend-performance-hotspots`

## Impact（剩余）

- 影响模块：
  - `backend/internal/service/gemini_messages_compat_service.go`
  - `backend/internal/service/ratelimit_service.go`
  - `backend/internal/service/gateway_service.go`
  - `backend/internal/service/account.go`
  - `backend/internal/repository/ops_repo_dashboard.go`
  - `backend/internal/repository/usage_log_repo.go`

- 验收闸门（发布前仍需压测确认）：
  - Gemini 预检分钟窗口 SQL 次数 <= 1/请求。
  - SSE 热路径 `allocs/op` 下降 >= 25%，`B/op` 下降 >= 20%。
  - Ops raw 查询 P95 下降 >= 30%，且超时可降级返回。
  - 模型映射热点调用 CPU 下降（以 pprof hot path 对比确认）。

- 发布与回滚：
  - 分域开关：`gemini-precheck-batch`、`sse-single-parse`、`ops-raw-cte`、`model-mapping-cache`。
  - 任一子域越界可单独回滚。
