# Phase 08 Context: Template Versioning & Rollback

## Objective
- 模板版本化存储并支持回滚（BULK-13）。

## Inputs
- Phase 7 模板实体与共享权限
- 现有批量 payload 语义与模板应用逻辑

## Risks
- 回滚语义与当前字段 enable 机制不一致
- 历史版本体积增长导致读写压力

## Deliverables
- 08-01 PLAN + SUMMARY
