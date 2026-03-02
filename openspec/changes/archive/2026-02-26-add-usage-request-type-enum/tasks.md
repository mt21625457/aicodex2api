## 1. 数据库迁移

- [ ] 1.1 新增迁移：`usage_logs.request_type SMALLINT NOT NULL DEFAULT 0`
- [ ] 1.2 增加 `request_type` 枚举值约束（CHECK）
- [ ] 1.3 增加索引 `idx_usage_logs_request_type_created_at`
- [ ] 1.4 编写批量回填脚本/SQL（按旧字段映射）
- [ ] 1.5 补充迁移集成测试（列存在、默认值、约束）
- [ ] 1.6 回填支持 dry-run 与分批参数（batch size/游标），并提供回填前后行数对账

## 2. 后端模型与仓储

- [ ] 2.1 在 `service.UsageLog` 增加 `RequestType` 字段与枚举类型
- [ ] 2.2 `usage_log_repo` 的 insert/select/scan 增加 `request_type`
- [ ] 2.3 写入链路实现双写：`request_type` + 旧字段
- [ ] 2.4 读取链路实现双读回退：`request_type=unknown` 时由旧字段推导
- [ ] 2.5 增加仓储集成测试覆盖 `request_type`

## 3. 后端 API 与筛选

- [ ] 3.1 DTO 新增 `request_type` 响应字段（保留 `stream`/`openai_ws_mode`）
- [ ] 3.2 用户 usage 列表接口新增 `request_type` 查询参数
- [ ] 3.3 管理员 usage 列表/统计接口新增 `request_type` 查询参数
- [ ] 3.4 dashboard trend/model stats 新增 `request_type` 查询参数
- [ ] 3.5 usage cleanup 过滤条件新增 `request_type`
- [ ] 3.6 明确并实现参数优先级：`request_type` 优先于 `stream`
- [ ] 3.7 统一 `request_type` 参数解析与校验（仅接受 `unknown/sync/stream/ws_v2`，非法值返回 400）
- [ ] 3.8 响应层实现兼容字段一致性映射（`request_type` 与 `stream/openai_ws_mode`）
- [ ] 3.9 补充 handler/service/repository 全链路测试（含非法参数、覆盖入口、优先级）

## 4. 前端改造

- [ ] 4.1 `frontend/src/types` 增加 `request_type` 类型定义
- [ ] 4.2 管理端筛选组件 `UsageFilters` 将“类型”升级为枚举筛选（含 WS）
- [ ] 4.3 用户端与管理端表格统一使用 `request_type` 渲染（保留旧字段回退）
- [ ] 4.4 导出逻辑统一使用枚举映射函数（CSV/Excel 同口径）
- [ ] 4.5 dashboard API 参数透传 `request_type`（用户端与管理员端 trend/models）
- [ ] 4.6 清理任务弹窗 `UsageCleanupDialog` 支持按 `request_type` 创建任务
- [ ] 4.7 实现老后端兼容回退（无 `request_type` 字段或不支持 `request_type` 查询参数时回退旧逻辑）
- [ ] 4.8 更新中英文 i18n 文案

## 5. 发布与观测

- [ ] 5.1 制定灰度计划：先 DB，再后端，再前端
- [ ] 5.2 增加一致性监控：`request_type` 与旧字段映射差异
- [ ] 5.3 准备回滚手册（前后端独立回滚）
- [ ] 5.4 上线前执行回填对账（按类型抽样比对 `request_type` 与旧字段映射）
- [ ] 5.5 上线后验证：类型展示、筛选、导出、统计、清理任务口径一致
