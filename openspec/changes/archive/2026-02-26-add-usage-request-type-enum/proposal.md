## Why

当前“使用记录 -> 类型”由两个布尔字段组合推导：`openai_ws_mode` 与 `stream`。该方案存在以下问题：

- 语义分散：类型不是单一事实源，展示层与数据层容易出现分支漂移。
- 扩展困难：新增协议类型（如 `ws_v1`、`grpc`、`batch`）时，需要在后端、前端、导出、筛选多处追加 if/else。
- 回归风险高：任何链路漏赋值都会导致展示错误。
- 筛选能力弱：当前筛选参数仅支持 `stream=true/false`，无法直接筛选 `WS`。

## What Changes

- 新增 usage 主枚举字段：`request_type`，作为“类型”唯一主事实源。
- 保留兼容字段：`stream`、`openai_ws_mode`（至少保留 2 个版本周期，不做破坏性删除）。
- 新增查询参数：`request_type`（列表、统计、趋势、模型统计、清理任务均支持）。
- `request_type` 参数仅接受 `unknown/sync/stream/ws_v2`（小写），非法值返回 `400 Bad Request`。
- 前端表格/导出/筛选升级为枚举驱动，同时保留旧字段回退逻辑。
- 响应保持兼容字段一致性：`request_type` 与 `stream/openai_ws_mode` 映射保持稳定。
- 提供历史数据回填与灰度升级方案，保证向前兼容、可回滚。

## 枚举定义（建议）

- `unknown` = 0
- `sync` = 1
- `stream` = 2
- `ws_v2` = 3
- 预留未来值：`ws_v1`、`grpc`、`batch`

## 兼容映射规则

- `openai_ws_mode=true` -> `ws_v2`
- `openai_ws_mode=false && stream=true` -> `stream`
- `openai_ws_mode=false && stream=false` -> `sync`

该映射与当前线上展示逻辑保持一致，确保历史口径不变。

## Capabilities

### New Capabilities

- `usage-request-type`: 使用记录类型由枚举统一建模，支持扩展与统一筛选。

## Impact

- Backend
  - 数据库：`usage_logs` 新增 `request_type` 列与索引、回填脚本。
  - 领域模型：`service.UsageLog`、DTO、查询过滤结构新增 `request_type`。
  - Repository：插入、读取、列表/统计/趋势/清理筛选支持 `request_type`。
  - Handler/API：新增 `request_type` 请求参数，保留 `stream` 参数兼容。
- Frontend
  - 类型定义新增 `request_type`。
  - 管理端筛选“类型”改为枚举选项（可筛 `WS`），旧接口回退兼容。
  - 用户端/管理端表格与导出统一走枚举渲染。
- Tests
  - 增加回填映射、双读回退、双参数兼容、筛选准确性测试。

## Rollout

1. 先发 DB 迁移（加列、索引、回填，不删旧字段）。
2. 再发后端双写双读（响应新增 `request_type`，旧字段继续返回）。
3. 最后发前端枚举化（带旧字段回退）。
4. 观察稳定后再评估旧字段淘汰计划。

## Rollback

- 任何阶段回滚到旧后端/旧前端均可运行：旧字段仍在且语义不变。
- 即使出现 `request_type=unknown` 历史写入，新版本读取也会按旧字段回退推导。
