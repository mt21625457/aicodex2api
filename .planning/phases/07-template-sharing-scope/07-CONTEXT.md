# Phase 07 Context: Template Sharing Scope

## Objective
- 建立模板服务端共享能力，支持团队/分组范围可见（BULK-12）。

## Inputs
- v1.1 本地模板实现：`frontend/src/components/account/bulkEditTemplateStore.ts`
- 账号与权限现有模型（admin/group）
- 现有批量编辑 scope 约束

## Risks
- 共享权限边界不清导致越权
- 与现有同类型校验冲突

## Deliverables
- 07-01 PLAN + SUMMARY
