## ADDED Requirements

### Requirement: CONCURRENTLY 索引迁移需走非事务执行路径
系统 SHALL 为 `CREATE INDEX CONCURRENTLY` / `DROP INDEX CONCURRENTLY` 提供非事务执行路径（如 `*_notx.sql` 迁移或等效机制），避免被默认事务迁移包装导致执行失败。

#### Scenario: 执行并发索引迁移
- **WHEN** 运行包含 `CREATE INDEX CONCURRENTLY` 的索引迁移
- **THEN** 迁移在非事务模式执行并成功完成，不触发“cannot run inside a transaction block”错误

#### Scenario: 事务迁移向前兼容
- **WHEN** 运行不包含 `CONCURRENTLY` 的历史事务迁移文件
- **THEN** 系统继续按原事务模式执行，不改变既有迁移语义

---

### Requirement: 非事务迁移文件必须幂等且语义隔离
系统 SHALL 对 `*_notx.sql` 执行幂等约束与语义隔离：创建索引使用 `CREATE INDEX CONCURRENTLY IF NOT EXISTS`，删除索引使用 `DROP INDEX CONCURRENTLY IF EXISTS`，并且同一迁移文件不得混入需要事务保护的 DDL/DML。

#### Scenario: 重复执行非事务索引迁移
- **WHEN** 运维重复执行同一 `*_notx.sql`（例如灾备演练或重放）
- **THEN** 迁移因 `IF NOT EXISTS`/`IF EXISTS` 具备幂等性，不因对象已存在/不存在而失败

#### Scenario: 校验迁移语义隔离
- **WHEN** 提交新的 `*_notx.sql` 迁移
- **THEN** 文件仅包含 `CONCURRENTLY` 相关语句，不混入事务语义 SQL，避免执行器行为不确定

---

### Requirement: accounts 表调度复合部分索引
系统 SHALL 在 `accounts` 表上创建复合部分索引以覆盖调度热路径查询：
- `(platform, priority) WHERE deleted_at IS NULL AND status = 'active' AND schedulable = true`
- `(priority, status) WHERE deleted_at IS NULL AND schedulable = true`（无平台过滤场景）

#### Scenario: 按平台查询可调度账号
- **WHEN** 调度器调用 `ListSchedulableByPlatform` 查询特定平台的可调度账号
- **THEN** PostgreSQL 使用 `idx_accounts_schedulable_hot` 部分索引执行 Index Scan，而非 Seq Scan 或低效 Bitmap Scan

#### Scenario: 全平台查询可调度账号
- **WHEN** 调度器调用 `ListSchedulable` 查询所有平台的可调度账号
- **THEN** PostgreSQL 使用 `idx_accounts_active_schedulable` 部分索引

---

### Requirement: user_subscriptions 表复合部分索引
系统 SHALL 在 `user_subscriptions` 表上创建复合部分索引：`(user_id, status, expires_at) WHERE deleted_at IS NULL`

#### Scenario: 查询用户活跃订阅
- **WHEN** 系统查询 `WHERE user_id = ? AND status = 'active' AND deleted_at IS NULL`
- **THEN** PostgreSQL 使用复合部分索引执行 Index Scan

---

### Requirement: usage_logs 表分组维度复合索引
系统 SHALL 在 `usage_logs` 表上创建复合索引：`(group_id, created_at) WHERE group_id IS NOT NULL`

#### Scenario: 按分组查询时间范围用量
- **WHEN** 仪表盘按分组维度查询用量统计
- **THEN** PostgreSQL 使用 `(group_id, created_at)` 复合索引，而非 `group_id` 单列索引 + Filter

---

### Requirement: loadTempUnschedStates 多余查询消除
系统 SHALL 先完成 Ent schema 与现有数据库列对齐（补齐 `temp_unschedulable_until`、`temp_unschedulable_reason` 字段定义），再在首次 Ent ORM 查询 accounts 时一并 Select 这两个字段，消除 `loadTempUnschedStates` 对 accounts 表的第二次查询。

#### Scenario: 批量加载账号信息
- **WHEN** `accountsToService` 或 `GetByIDs` 加载账号列表
- **THEN** 系统通过单次 ORM 查询获取所有需要的字段（含 temp_unschedulable 相关），不再执行 `loadTempUnschedStates` 的额外 SQL 查询

---

### Requirement: 仪表盘 SQL 查询合并
系统 SHALL 将 `fillDashboardUsageStatsFromUsageLogs` 中的 4 次独立 SQL 查询合并为 1-2 个 CTE 查询，减少数据库往返。

#### Scenario: 仪表盘用量统计查询
- **WHEN** 系统调用 `fillDashboardUsageStatsFromUsageLogs` 获取仪表盘数据
- **THEN** 系统通过单个 CTE 查询同时获取总体统计、今日统计、今日活跃用户数和小时活跃用户数，而非 4 次独立查询

---

### Requirement: deleted_at 单列索引替换为业务部分索引
系统 SHALL 评估并清理软删除表上无效的 `deleted_at` 单列索引，将其替换为业务查询复合索引中的 `WHERE deleted_at IS NULL` 部分索引条件。

#### Scenario: accounts 表 deleted_at 索引优化
- **WHEN** accounts 表已有业务复合部分索引（含 `WHERE deleted_at IS NULL` 条件）
- **THEN** 原 `deleted_at` 单列索引可安全移除，减少写入时的索引维护开销

#### Scenario: 移除前观察门禁
- **WHEN** 计划删除任意表 `deleted_at` 单列索引
- **THEN** 系统先完成至少 7 天慢 SQL/查询计划观测，确认无关键查询依赖该单列索引后再执行删除

#### Scenario: 删除后回滚
- **WHEN** 删除 `deleted_at` 单列索引后出现查询退化
- **THEN** 系统可通过 `CREATE INDEX CONCURRENTLY IF NOT EXISTS` 在低峰期快速恢复索引

---

### Requirement: UpdateSortOrders 批量化
系统 SHALL 将 `UpdateSortOrders` 从逐条 UPDATE 优化为单条批量 UPDATE SQL（使用 `CASE WHEN` 或 `unnest` 方式）。

#### Scenario: 批量更新分组排序
- **WHEN** 管理员调整 N 个分组的排序顺序
- **THEN** 系统通过单条 SQL 完成所有排序更新，而非 N 次独立 UPDATE
