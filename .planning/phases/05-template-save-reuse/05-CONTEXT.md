# Phase 05 Context: Template Save & Reuse

## Objective
- 支持保存/复用批量编辑模板（BULK-10）。

## Inputs
- Bulk payload builder: `frontend/src/components/account/bulkEditPayload.ts`
- Bulk update API contract: `frontend/src/api/admin/accounts.ts`
- Backend bulk update handler/service/repo chain

## Risks
- 模板字段跨类型复用导致不兼容
- 应用模板覆盖未勾选字段

## Deliverables
- 05-01 PLAN + SUMMARY
