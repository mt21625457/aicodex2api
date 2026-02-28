# Summary 08-01: 模板版本历史与回滚

## 完成状态

- ✅ 完成 Phase 8 / Plan 08-01
- ✅ 达成 BULK-13（模板版本历史 + 一键回滚）

## 交付内容

### 后端

- 模板存储结构扩展：`versions[]` 历史快照（settings JSON）
- 更新模板时自动快照旧版本（更新前入栈历史）
- 新增服务能力：
  - `ListBulkEditTemplateVersions`
  - `RollbackBulkEditTemplate`
- 新增 API：
  - `GET /api/v1/admin/settings/bulk-edit-templates/:template_id/versions`
  - `POST /api/v1/admin/settings/bulk-edit-templates/:template_id/rollback`
- 版本可见性与模板可见性一致：`private/team/groups + scope_group_ids` 交集判断

### 前端

- 扩展模板 API：
  - `getBulkEditTemplateVersions`
  - `rollbackBulkEditTemplate`
- 在批量编辑模板区域增加“版本历史”面板
- 支持回滚确认、回滚中状态、回滚后自动刷新模板与历史
- 增补中英文文案（版本加载、空态、回滚确认/成功/失败）

## 测试与验证

### Backend

- `cd backend && go test ./internal/service ./internal/handler/admin -run 'SettingServiceBulkEditTemplate|SettingHandlerBulkEditTemplate|ParseScopeGroupIDs'`
- 新增覆盖：
  - 版本链路（更新生成版本、回滚成功、回滚后历史可追溯）
  - 权限边界（groups 交集可见、无交集拒绝）
  - 错误分支（版本不存在、参数错误、未授权）

### Frontend

- `cd frontend && pnpm test:run src/api/__tests__/settings.bulkEditTemplates.spec.ts ...`
- `cd frontend && pnpm typecheck`
- `cd frontend && pnpm build`
- 新增 API 单测覆盖 `versions/rollback` 请求与参数拼装

## 结论

Phase 8 已完成，模板具备“可追溯 + 可回退”治理能力，可进入 Phase 9 质量门禁与发布清单阶段。
