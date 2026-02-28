# Milestones

## v1.0 OpenAI Bulk Edit Parity (Shipped: 2026-02-28)

**Phases completed:** 3 phases, 3 plans, 0 tasks

**Key accomplishments:**
- 后端批量更新链路补齐 `auto_pause_on_expired` 字段透传（handler → service → repository）
- 新增 OpenAI 专属字段批量安全校验：`account_ids` 必须同平台且同类型（oauth/apikey）
- 前端批量编辑支持 OpenAI 范围化编辑入口，按 `platform:type` 分流到对应编辑页
- 批量 payload 维持“勾选才发送”语义，并支持显式关闭（`false`/`off`）回滚配置
- 新增 UAT 清单与前后端单测，核心改动文件覆盖率达到并超过 85% 目标

---

## v1.1 Bulk Edit Productivity (Completed: 2026-02-28)

**Phases completed:** 3 phases, 3 plans

**Key accomplishments:**
- 范围弹窗新增 `platform:type` 分组统计与同类约束提示，非同类账号不会进入批量编辑
- 批量编辑弹窗支持模板保存/应用/删除，模板按 `platform:type` 严格隔离
- 模板状态新增归一化层，保证数据可回放且不破坏“勾选才提交”语义
- 新增模板状态与模板存储单测，形成 v1.1 UAT 清单
- v1.1 核心文件覆盖率验证达标（Statements 99.55%, Branches 98.08%, Functions 100%, Lines 99.55%）
