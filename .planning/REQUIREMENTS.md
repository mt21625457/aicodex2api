# Requirements: Template Collaboration & Versioning (v1.2)

**Defined:** 2026-02-28  
**Core Value:** 批量模板可协作、可追溯、可回滚  
**Previous Milestone Archive:** `.planning/milestones/v1.1-REQUIREMENTS.md`

## v1.2 Requirements

### Collaboration

- [x] **BULK-12**: 管理员可将批量编辑模板共享到指定团队或分组，并控制可见范围

### Governance

- [x] **BULK-13**: 模板支持版本历史查看与一键回滚到历史版本

### Quality Gate

- [x] **BULK-14**: 模板共享与回滚能力必须具备自动化测试与最小 UAT 清单，核心改动覆盖率维持 ≥85%

## Future Requirements

- **BULK-15**: 模板操作审计日志（创建/更新/删除/回滚）
- **BULK-16**: 模板变更审批流程（双人确认）

## Out of Scope

| Feature | Reason |
|---------|--------|
| 跨租户模板共享 | 先保证单租户内安全边界 |
| 复杂权限模型（字段级 ACL） | v1.2 先做团队/分组级共享 |

## Traceability

| Requirement | Phase | Status |
|-------------|-------|--------|
| BULK-12 | Phase 7 | Completed |
| BULK-13 | Phase 8 | Completed |
| BULK-14 | Phase 9 | Completed |
