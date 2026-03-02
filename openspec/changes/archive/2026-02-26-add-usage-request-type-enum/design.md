## Context

当前代码中“类型”由 `stream` 和 `openai_ws_mode` 组合推导，且筛选只支持 `stream`。
这导致类型语义分散在数据库、后端 DTO、前端展示、导出逻辑中，扩展和维护成本高。

本设计目标是在不破坏现有接口的前提下，引入 `request_type` 作为主枚举字段，实现：

- 单一事实源
- 可扩展类型体系
- 向前兼容升级
- 可回滚

## Goals / Non-Goals

### Goals

- 为 usage 记录建立统一枚举字段 `request_type`。
- 兼容历史数据与旧客户端（旧字段、旧参数仍可用）。
- 支持新筛选维度（列表/统计/趋势/模型/清理）。
- 全链路灰度发布，避免中断与大回归。

### Non-Goals

- 本期不删除 `stream`、`openai_ws_mode` 字段。
- 本期不强制所有调用方立刻改用新参数。

## Data Model Design

### 数据库

`usage_logs` 新增字段：

- `request_type SMALLINT NOT NULL DEFAULT 0`

并新增：

- `CHECK (request_type IN (0,1,2,3))`
- 索引：`idx_usage_logs_request_type_created_at(request_type, created_at)`

当前枚举编码：

- `0=unknown`
- `1=sync`
- `2=stream`
- `3=ws_v2`

说明：本期 CHECK 仅覆盖已落地值。未来新增类型（如 `ws_v1`、`grpc`、`batch`）时，通过新迁移扩展 CHECK 与映射，不影响当前兼容策略。

### 回填策略

按批回填，避免长事务：

- `openai_ws_mode=true` -> `3(ws_v2)`
- `openai_ws_mode=false and stream=true` -> `2(stream)`
- else -> `1(sync)`

### 读写策略

- 写入：后端双写（新字段 + 旧字段）。
- 读取：优先读 `request_type`；若为 `unknown`，按旧字段推导。

## API Compatibility Design

### 响应字段

新增：

- `request_type`（字符串枚举：`sync`/`stream`/`ws_v2`/`unknown`）

保留：

- `stream`
- `openai_ws_mode`

### 请求参数

新增：

- `request_type`（字符串枚举）

保留：

- `stream`

### 参数语义与校验

- `request_type` 可选值为：`unknown`、`sync`、`stream`、`ws_v2`。
- `request_type` 采用小写规范值；非法值 MUST 返回 `400 Bad Request`，并返回可接受值列表。
- 当同时传入 `request_type` 与 `stream` 时，按 `request_type` 过滤，`stream` 仅用于兼容旧客户端。
- 为避免接口行为漂移，`request_type` 参数解析在用户侧与管理侧复用同一校验逻辑。

### 参数优先级

- 当 `request_type` 存在时，优先按 `request_type` 过滤。
- `stream` 继续支持，用于旧客户端。

该策略确保新客户端可直接枚举筛选，旧客户端行为不变。

### 过滤入口覆盖范围

`request_type` 过滤能力覆盖以下入口：

- 用户 usage 列表：`GET /api/v1/usage`
- 管理员 usage 列表/统计：`GET /api/v1/admin/usage`、`GET /api/v1/admin/usage/stats`
- 用户 dashboard 趋势/模型：`GET /api/v1/usage/dashboard/trend`、`GET /api/v1/usage/dashboard/models`
- 管理员 dashboard 趋势/模型：`GET /api/v1/admin/dashboard/trend`、`GET /api/v1/admin/dashboard/models`
- usage cleanup 任务：`POST /api/v1/admin/usage/cleanup-tasks`（过滤条件）

### 兼容字段一致性

为确保旧客户端口径稳定，响应中 `stream/openai_ws_mode` 与 `request_type` 的关系必须一致：

- `request_type=ws_v2` -> `openai_ws_mode=true`
- `request_type=stream` -> `openai_ws_mode=false && stream=true`
- `request_type=sync` -> `openai_ws_mode=false && stream=false`
- `request_type=unknown` -> 按历史旧字段回退推导，不强制改写存量值

## Frontend Design

### 展示

- 类型徽标与文案优先使用 `request_type`。
- 若响应没有 `request_type`（老后端），回退旧逻辑：
  - `openai_ws_mode ? ws : stream ? stream : sync`

### 筛选

管理端类型筛选改为枚举选项：

- 全部
- 同步（sync）
- 流式（stream）
- WS（ws_v2）

### 导出

CSV/Excel 导出与表格使用同一 `resolveRequestTypeLabel`，避免口径不一致。

## Upgrade / Rollback Plan

### Upgrade

1. DB migration：加列 + 索引 + 回填。
2. Backend：双写双读 + 新参数 + 新响应字段。
3. Frontend：枚举渲染 + 枚举筛选 + 旧字段回退。

### Rollback

- 回滚前端：后端仍返回旧字段，不影响。
- 回滚后端：数据库保留旧字段，系统可继续运行。
- 由于不删旧字段，回滚无需数据修复。

## Risks and Mitigations

- 风险：新旧字段短期不一致。
  - 缓解：读取优先新字段，`unknown` 自动回退旧字段推导；增加一致性监控。
- 风险：大表回填锁与性能波动。
  - 缓解：按 ID 批量回填，低峰执行，监控慢 SQL 与复制延迟。
- 风险：多入口筛选遗漏。
  - 缓解：统一扩展过滤结构体，覆盖列表/统计/趋势/模型/清理所有入口测试。

## Testing Strategy

- 单元测试
  - `request_type <-> 旧字段` 映射
  - DTO 回退逻辑
  - handler 参数解析、非法值校验与参数优先级
- 仓储集成测试
  - 插入/读取 `request_type`
  - 列表/统计/趋势/模型/清理按 `request_type` 过滤正确
- 回归测试
  - 仅用旧参数 `stream` 的行为不变
  - 前端在无 `request_type` 响应时显示不变
  - 响应 `request_type` 与 `stream/openai_ws_mode` 一致性不回归
