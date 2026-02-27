## sora-client-s3-storage 三轮审核记录

### 第 1 轮：一致性审核（proposal/design/tasks/specs）

发现问题：
- 关键术语不一致：`四个核心问题` 实际列了 5 条。
- 能力名不一致：`sora-gateway` 与现有规格能力 `sora-generation-gateway` 不一致。
- 配额键名不一致：`sora_default_quota_bytes` 与 `sora_default_storage_quota_bytes` 混用。
- 数据模型表述歧义：`user_sora_quotas 表或 users 字段` 两种方案并存。
- 路径/文件名不一致：`handler/admin/settings_handler.go` 与仓库实际 `handler/admin/setting_handler.go` 不一致。

修复动作：
- 统一 proposal 术语、能力名、配置键名、字段命名与文件路径。
- 删除数据库方案歧义，明确采用 `users` 与 `groups` 字段方案。

### 第 2 轮：可实施性审核（结构与迁移）

发现问题：
- `sora_generations` 使用 `(user_id, created_at)` 联合唯一约束，存在高并发写入冲突风险。
- 分组配额字段命名未带单位后缀（`sora_storage_quota`），与 `*_bytes` 体系不一致。
- 任务清单缺少“路径 A 不落盘”的明确改造任务，无法保证 `/sora/v1/chat/completions` 纯透传目标。

修复动作：
- 将联合唯一约束改为普通索引 `(user_id, created_at DESC)`。
- 统一分组字段为 `groups.sora_storage_quota_bytes`。
- 在 `tasks.md` 增加路径 A 不落盘任务与对应验证项。

### 第 3 轮：鲁棒性审核（边界与运维）

发现问题：
- apikey 透传 URL 拼接写法为字符串拼接，存在双斜杠与非法 base_url 风险。
- S3 访问 URL 策略未收敛（CDN 与预签名都提到，但缺少决策规则）。
- 配额并发控制表述中默认容忍超额，与严格配额目标冲突。

修复动作：
- 在 proposal/design/spec 中补充 `base_url` 校验（必填 + scheme）与规范化拼接要求。
- 在 `sora-s3-media-storage/spec.md` 明确“CDN 优先，预签名兜底”策略。
- 将配额并发策略改为“原子更新失败即回滚文件并报错”，取消容忍超额表述。

### 结论

- 已完成 3 轮审核并修复全部已发现问题。
- 当前提案在一致性、可实施性和鲁棒性三个维度均已收敛，且已同步更新 `proposal.md`、`design.md`、`tasks.md` 与相关 specs。
