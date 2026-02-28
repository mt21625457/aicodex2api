# Summary 07-01: 模板服务端共享（团队/分组范围）

## 完成状态

- ✅ 完成 Phase 7 / Plan 07-01
- ✅ 达成 BULK-12（模板共享范围）

## 交付内容

### 后端

- 新增 setting key：`bulk_edit_template_library_v1`
- 新增模板库服务：
  - `ListBulkEditTemplates`
  - `UpsertBulkEditTemplate`
  - `DeleteBulkEditTemplate`
- 新增模板 API：
  - `GET /api/v1/admin/settings/bulk-edit-templates`
  - `POST /api/v1/admin/settings/bulk-edit-templates`
  - `DELETE /api/v1/admin/settings/bulk-edit-templates/:template_id`
- 路由已注册到 admin settings 分组

### 前端

- 新增 API 模块：`frontend/src/api/admin/bulkEditTemplates.ts`
- 批量编辑模板区接入服务端模板库
- 新增模板共享范围 UI：
  - 仅自己（private）
  - 团队管理员（team）
  - 分组可见（groups）
- AccountsView 在确认批量范围时计算命中账号分组并透传到模板请求，实现 groups 模板可见性过滤

### 兼容性/安全边界

- 保持“同平台 + 同类型”批量编辑约束不变
- groups 模板仅在 `scope_group_ids` 有交集时可见
- private 模板仅创建者可见

## 测试与验证

### Backend

- `go test ./internal/service ./internal/handler/admin -run 'SettingServiceBulkEditTemplate|SettingHandlerBulkEditTemplate|ParseScopeGroupIDs|AccountHandlerBulkUpdate'`
- 新增测试文件：
  - `backend/internal/service/setting_bulk_edit_template_test.go`
  - `backend/internal/handler/admin/setting_handler_bulk_edit_template_test.go`
- 关键覆盖（函数级）：
  - `setting_bulk_edit_template.go` 关键函数（list/upsert/delete/load）≥ 87%
  - `setting_handler_bulk_edit_template.go` 核心 handler 100%，解析函数 90%

### Frontend

- `pnpm test:run src/components/account/__tests__ src/api/__tests__/settings.bulkEditTemplates.spec.ts src/views/__tests__/accountsBulkEditScope.spec.ts`
- `pnpm typecheck`
- 覆盖率（文件级，目标改动）
  - `bulkEditTemplates.ts`: 100%
  - `bulkEditTemplateRemoteMapper.ts`: 100%
  - `bulkEditTemplateStore.ts`: 96.73%
  - `accountsBulkEditScope.ts`: 100%

## 结论

Phase 7 已满足里程碑目标，下一步进入 Phase 8（模板版本历史与回滚）。
