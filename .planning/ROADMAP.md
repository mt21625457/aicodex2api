# Roadmap: Template Collaboration & Versioning (v1.2)

## Milestones

- ✅ **v1.0 OpenAI Bulk Edit Parity** — shipped 2026-02-28 (archive: `.planning/milestones/v1.0-ROADMAP.md`)
- ✅ **v1.1 Bulk Edit Productivity** — completed 2026-02-28 (archive: `.planning/milestones/v1.1-ROADMAP.md`)
- ✅ **v1.2 Template Collaboration & Versioning** — completed 2026-02-28

## Overview

本里程碑聚焦“模板协作与治理”。在 v1.1 本地模板复用基础上，补齐服务端共享、版本回滚与质量门禁，确保模板可团队化使用并具备可追溯性。

## Phases

**Phase Numbering:**
- Integer phases continue from previous milestone (Phase 7+)
- Decimal phases (7.1, 7.2) reserved for urgent insertions

- [x] **Phase 7: Template Sharing Scope** - 模板共享到团队/分组并限制可见范围
- [x] **Phase 8: Template Versioning & Rollback** - 模板版本历史、差异预览与回滚
- [x] **Phase 9: Quality Gate & Rollout (v1.2)** - 测试补齐、覆盖率、UAT 与上线说明
- [ ] **Phase 10: OpenAI WSv2 ctx_pool normalization hardening** - 归一化收敛、跨实例 pending 状态、invariant-first 判定与回归矩阵

## Phase Details

### Phase 7: Template Sharing Scope
**Goal:** 建立服务端模板实体与共享范围控制，让模板可被团队内复用。
**Depends on:** Existing scoped bulk-edit + local template workflow
**Requirements**: [BULK-12]
**Success Criteria** (what must be TRUE):
  1. 管理员可创建共享模板并指定作用范围（团队/分组）。
  2. 非授权范围用户看不到模板。
  3. 与现有同平台同类型校验兼容。
**Plans**: 1 plan

Plans:
- [x] 07-01: 模板服务端 schema + API + 权限校验

### Phase 8: Template Versioning & Rollback
**Goal:** 引入模板版本历史，支持回滚并保留变更记录。
**Depends on:** Phase 7
**Requirements**: [BULK-13]
**Success Criteria** (what must be TRUE):
  1. 模板每次更新自动生成新版本。
  2. 管理员可查看版本列表并回滚。
  3. 回滚后模板配置与提交 payload 语义一致。
**Plans**: 1 plan

Plans:
- [x] 08-01: 版本存储 + 回滚接口 + 前端版本面板

### Phase 9: Quality Gate & Rollout (v1.2)
**Goal:** 为共享与回滚能力补齐自动化验证与交付清单，确保可发布。
**Depends on:** Phase 8
**Requirements**: [BULK-14]
**Success Criteria** (what must be TRUE):
  1. 共享/回滚关键链路有单元测试覆盖。
  2. 核心改动覆盖率 ≥85%。
  3. 提供可复用 UAT 清单（权限边界、回滚成功/失败、异常处理）。
**Plans**: 1 plan

Plans:
- [x] 09-01: 测试补齐 + 覆盖率报告 + UAT 文档

## Progress

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 7. Template Sharing Scope | 1/1 | Completed | 2026-02-28 |
| 8. Template Versioning & Rollback | 1/1 | Completed | 2026-02-28 |
| 9. Quality Gate & Rollout (v1.2) | 1/1 | Completed | 2026-02-28 |
| 10. OpenAI WSv2 ctx_pool normalization hardening | 0/1 | Planned | - |

### Phase 10: OpenAI WSv2 ctx_pool normalization hardening

**Goal:** 收敛 ctx_pool WSv2 发送前归一化路径，补齐跨实例 pending 状态与不变量优先判定，保持恢复矩阵稳定。
**Requirements**: [WSV2-CTX-NORMALIZER, WSV2-CTX-PENDING-CROSS-INSTANCE, WSV2-CTX-INVARIANT-FIRST, WSV2-CTX-RECOVERY-MATRIX, WSV2-CTX-REGRESSION-MATRIX]
**Depends on:** Phase 9
**Plans:** 1 plan

Plans:
- [ ] 10-01: 统一 normalizer + 跨实例 pending_call_ids + invariant-first + 回归矩阵
