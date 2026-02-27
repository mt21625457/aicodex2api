## ADDED Requirements

### Requirement: 用户存储配额字段
系统 SHALL 在 `users` 表新增 Sora 存储配额字段，用于追踪每个用户的配额和用量。

#### Scenario: 用户表新增配额字段
- **WHEN** 数据库迁移执行
- **THEN** `users` 表 SHALL 新增 `sora_storage_quota_bytes BIGINT NOT NULL DEFAULT 0` 字段（0 表示使用系统默认）
- **AND** `users` 表 SHALL 新增 `sora_storage_used_bytes BIGINT NOT NULL DEFAULT 0` 字段

### Requirement: 系统默认配额设置
系统 SHALL 提供全局默认 Sora 存储配额设置，管理员可在系统设置中配置。

#### Scenario: 管理员设置全局默认配额
- **WHEN** 管理员在系统设置中设置 `sora_default_storage_quota_bytes`
- **THEN** 系统 SHALL 将该值保存到 Settings 表
- **AND** 所有未单独设置配额的用户 SHALL 使用该默认值

#### Scenario: 未设置全局默认配额
- **WHEN** `sora_default_storage_quota_bytes` 未设置或为 0
- **THEN** 系统 SHALL 不限制用户存储空间（即无配额限制）

### Requirement: 配额优先级判断
系统 SHALL 按用户级 → 分组级 → 系统默认的优先级计算有效配额。

#### Scenario: 用户级配额优先
- **WHEN** 用户 `sora_storage_quota_bytes > 0`
- **THEN** 有效配额 SHALL 为用户级配额值

#### Scenario: 分组级配额次优先
- **WHEN** 用户 `sora_storage_quota_bytes = 0`（未单独设置）
- **AND** 用户所属分组 `sora_storage_quota_bytes > 0`
- **THEN** 有效配额 SHALL 为分组级配额值

#### Scenario: 系统默认配额兜底
- **WHEN** 用户和分组的配额均未设置（均为 0）
- **THEN** 有效配额 SHALL 为 `settings.sora_default_storage_quota_bytes`

### Requirement: 生成前配额检查
系统 SHALL 在客户端 UI 调用路径发起生成前检查存储配额。

#### Scenario: 配额充足允许生成
- **WHEN** 用户发起 Sora 客户端生成请求
- **AND** `sora_storage_used_bytes < 有效配额`
- **THEN** 系统 SHALL 允许生成请求继续

#### Scenario: 配额不足拒绝生成
- **WHEN** 用户发起 Sora 客户端生成请求
- **AND** `sora_storage_used_bytes >= 有效配额`
- **AND** 有效配额 > 0
- **THEN** 系统 SHALL 返回 HTTP 429 错误
- **AND** 响应 SHALL 包含 `{ quota_bytes, used_bytes, message: "存储配额已满，请删除不需要的作品释放空间" }`
- **AND** 响应 SHALL 包含 `guide: "delete_works"` 字段，前端据此显示引导对话框

#### Scenario: 无配额限制时不检查
- **WHEN** 有效配额 = 0（系统默认也未设置）
- **THEN** 系统 SHALL 跳过配额检查，允许生成

### Requirement: 配额原子更新
系统 SHALL 使用原子操作更新用户已用存储空间，防止并发超额。

#### Scenario: 生成完成后累加用量
- **WHEN** 媒体文件上传到 S3/本地存储成功
- **THEN** 系统 SHALL 在计算出 `effective_quota` 后执行原子 SQL：`UPDATE users SET sora_storage_used_bytes = sora_storage_used_bytes + :file_size WHERE id = :id AND (:effective_quota = 0 OR sora_storage_used_bytes + :file_size <= :effective_quota)`
- **AND** 若原子更新失败（超额），系统 SHALL 删除已上传的文件并返回配额错误

#### Scenario: 删除作品后释放配额
- **WHEN** 用户删除一条生成记录
- **AND** 该记录 `file_size_bytes > 0`
- **THEN** 系统 SHALL 执行 `UPDATE users SET sora_storage_used_bytes = sora_storage_used_bytes - file_size WHERE id = ?`
- **AND** `sora_storage_used_bytes` SHALL 不低于 0

### Requirement: 配额查询 API
系统 SHALL 提供配额查询接口，用户可查看当前用量和剩余空间。

#### Scenario: 查询用户 Sora 配额
- **WHEN** 用户请求 `GET /api/v1/sora/quota`
- **THEN** 系统 SHALL 返回 `{ quota_bytes, used_bytes, available_bytes, quota_source }`
- **AND** `quota_source` SHALL 标明配额来源（"user" / "group" / "system" / "unlimited"）

### Requirement: 管理员配额管理
管理员 SHALL 可以在用户管理和分组管理中设置 Sora 存储配额。

#### Scenario: 管理员设置单个用户配额
- **WHEN** 管理员在用户编辑页面设置 Sora 存储配额
- **THEN** 系统 SHALL 更新 `users.sora_storage_quota_bytes`

#### Scenario: 管理员设置分组配额
- **WHEN** 管理员在分组管理中设置 Sora 存储配额
- **THEN** 系统 SHALL 更新 `groups.sora_storage_quota_bytes` 字段
- **AND** 该分组下所有未单独设置配额的用户 SHALL 使用分组配额
