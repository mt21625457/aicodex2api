# OpenAI Bulk Edit Productivity

## What This Is

在现有 OpenAI/多平台批量编辑能力上持续提升“安全性 + 复用效率 + 团队协作”。
当前系统已支持同平台同类型范围化批量编辑、模板保存复用和质量门禁，下一步聚焦模板协作与版本治理。

## Core Value

批量操作必须“看得清、改得快、可回退”。

## Current Milestone: v1.2 Template Collaboration & Versioning

**Goal:** 让批量编辑模板可在团队内共享并支持版本回滚，进一步降低重复配置和误改风险。

**Target features:**
- ⏳ 模板支持共享到团队/分组（BULK-12）
- ⏳ 模板支持版本历史与回滚（BULK-13）
- ⏳ 模板共享/回滚链路测试与上线验收（BULK-14）

## Milestone History

- ✅ v1.0 OpenAI Bulk Edit Parity (shipped 2026-02-28)
  - Archive: `.planning/milestones/v1.0-ROADMAP.md`
  - Archive: `.planning/milestones/v1.0-REQUIREMENTS.md`
- ✅ v1.1 Bulk Edit Productivity (completed 2026-02-28)
  - Archive: `.planning/milestones/v1.1-ROADMAP.md`
  - Archive: `.planning/milestones/v1.1-REQUIREMENTS.md`

## Constraints

- **Compatibility**: 不破坏现有 scoped bulk-edit 提交流程与 payload 语义
- **Safety**: 保持后端“同平台同类型”强校验
- **Testing**: 每个 phase 必须有自动化验证证据
- **Migration Cost**: 优先最小化 schema/API 变更，逐步演进

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| OpenAI 专属批量字段由 `extra` 统一承载 | 与单账号编辑实现保持一致，避免新列和迁移 | Completed (v1.0) |
| 专属字段在后端做强校验（同平台同类型） | 前端选择可能跨页，后端校验更可靠 | Completed (v1.0) |
| v1.1 先做范围预览再做模板复用 | 先降低误操作，再提升效率，风险更可控 | Completed (v1.1) |
| v1.1 模板先采用 localStorage 持久化 | 避免后端 schema/API 变更，快速交付并保持低风险 | Completed (v1.1) |
| v1.2 引入服务端模板共享与版本历史 | 支撑团队协作与回滚，补齐治理能力 | Pending |

---
*Last updated: 2026-02-28 for v1.2 milestone kickoff*
