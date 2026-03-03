## ADDED Requirements

### Requirement: 生成记录数据模型
系统 SHALL 新建 `sora_generations` 表存储每次 Sora 客户端 UI 生成的元数据。

#### Scenario: 数据库表创建
- **WHEN** 数据库迁移执行
- **THEN** 系统 SHALL 创建 `sora_generations` 表，包含以下字段：
  - `id` (BIGSERIAL PRIMARY KEY)
  - `user_id` (BIGINT NOT NULL, FK → users.id ON DELETE CASCADE)
  - `api_key_id` (BIGINT, 可空)
  - `model` (VARCHAR(64) NOT NULL)
  - `prompt` (TEXT NOT NULL DEFAULT '')
  - `media_type` (VARCHAR(16) NOT NULL DEFAULT 'video')
  - `status` (VARCHAR(16) NOT NULL DEFAULT 'pending')
  - `media_url` (TEXT NOT NULL DEFAULT '')
  - `media_urls` (JSONB, 多图 URL 数组)
  - `file_size_bytes` (BIGINT NOT NULL DEFAULT 0)
  - `storage_type` (VARCHAR(16) NOT NULL DEFAULT 'none')
  - `s3_object_keys` (JSONB, S3 object key 数组)
  - `upstream_task_id` (VARCHAR(128) NOT NULL DEFAULT '')
  - `error_message` (TEXT NOT NULL DEFAULT '')
  - `created_at` (TIMESTAMPTZ NOT NULL DEFAULT NOW())
  - `completed_at` (TIMESTAMPTZ)
- **AND** SHALL 创建 `(user_id, created_at DESC)` 普通索引（非唯一）
- **AND** SHALL 创建 `(user_id, status)` 索引

### Requirement: 创建生成记录
系统 SHALL 在客户端 UI 发起生成时创建记录，并在生成过程中更新状态。

#### Scenario: 发起生成时创建 pending 记录
- **WHEN** 用户通过 `POST /api/v1/sora/generate` 发起生成
- **THEN** 系统 SHALL 在 `sora_generations` 中创建一条 `status = 'pending'` 的记录
- **AND** 记录 SHALL 包含 `user_id`、`model`、`prompt`、`media_type`

#### Scenario: 上游开始处理时更新为 generating
- **WHEN** 上游开始处理生成任务
- **THEN** 系统 SHALL 更新记录 `status = 'generating'`
- **AND** 记录 `upstream_task_id`

#### Scenario: 生成成功时更新为 completed
- **WHEN** 生成完成且媒体文件存储成功
- **THEN** 系统 SHALL 更新记录 `status = 'completed'`
- **AND** 更新 `media_url`、`media_urls`、`file_size_bytes`、`storage_type`、`s3_object_keys`、`completed_at`

#### Scenario: 生成失败时更新为 failed
- **WHEN** 生成过程中发生错误
- **THEN** 系统 SHALL 更新记录 `status = 'failed'`
- **AND** 记录 `error_message`

#### Scenario: 用户取消生成
- **WHEN** 用户通过 `POST /api/v1/sora/generations/:id/cancel` 取消任务
- **AND** 记录状态为 `pending` 或 `generating`
- **THEN** 系统 SHALL 更新记录 `status = 'cancelled'`
- **AND** SHALL 不累加配额

#### Scenario: 手动保存到存储后更新
- **WHEN** 用户对 `storage_type = 'upstream'` 的记录手动触发保存
- **AND** S3 上传成功
- **THEN** 系统 SHALL 更新 `storage_type = 's3'`、`s3_object_keys`、`file_size_bytes`
- **AND** 累加存储配额

### Requirement: 查询生成历史列表
系统 SHALL 提供分页查询用户生成历史的 API。

#### Scenario: 获取用户生成历史
- **WHEN** 用户请求 `GET /api/v1/sora/generations`
- **THEN** 系统 SHALL 返回当前用户的生成记录列表，按 `created_at DESC` 排序
- **AND** 支持分页参数 `page`（默认 1）和 `page_size`（默认 20，最大 100）

#### Scenario: 按媒体类型筛选
- **WHEN** 请求携带 `media_type=video` 或 `media_type=image`
- **THEN** 系统 SHALL 只返回对应类型的记录

#### Scenario: 按状态筛选
- **WHEN** 请求携带 `status=completed`
- **THEN** 系统 SHALL 只返回对应状态的记录

#### Scenario: 按存储类型筛选（作品库专用）
- **WHEN** 请求携带 `storage_type=s3,local`
- **THEN** 系统 SHALL 返回已持久化存储（S3 或本地）的记录
- **AND** 作品库页面默认 SHALL 使用 `storage_type=s3,local` 筛选，展示所有已保存的作品
- **AND** `storage_type='upstream'` 和 `'none'` 的记录 SHALL 不在作品库中显示

#### Scenario: 预签名 URL 动态生成
- **WHEN** 返回 `storage_type = 's3'` 的记录列表
- **AND** 未配置 CDN URL
- **THEN** 系统 SHALL 为每条记录动态生成新的 S3 预签名 URL（24 小时有效）
- **AND** 前端 SHALL 不缓存媒体 URL

#### Scenario: 恢复进行中的任务
- **WHEN** 请求携带 `status=pending,generating`
- **THEN** 系统 SHALL 返回用户所有进行中的生成任务
- **AND** 前端页面加载时 SHALL 调用此接口恢复任务进度显示

### Requirement: 查询生成详情
系统 SHALL 提供查询单条生成记录详情的 API。

#### Scenario: 获取生成详情
- **WHEN** 用户请求 `GET /api/v1/sora/generations/:id`
- **AND** 该记录属于当前用户
- **THEN** 系统 SHALL 返回完整的生成记录详情

#### Scenario: 访问他人记录返回 404
- **WHEN** 用户请求的生成记录不属于当前用户
- **THEN** 系统 SHALL 返回 HTTP 404

### Requirement: 删除生成记录
系统 SHALL 提供删除生成记录的 API，并联动清理存储文件和配额。

#### Scenario: 删除单条记录
- **WHEN** 用户请求 `DELETE /api/v1/sora/generations/:id`
- **AND** 该记录属于当前用户
- **THEN** 系统 SHALL 删除数据库记录
- **AND** 若 `storage_type = 's3'`，SHALL 删除 S3 文件
- **AND** 若 `storage_type = 'local'`，SHALL 删除本地文件
- **AND** SHALL 释放对应的存储配额

#### Scenario: 删除不存在的记录
- **WHEN** 记录不存在或不属于当前用户
- **THEN** 系统 SHALL 返回 HTTP 404

### Requirement: 无存储模式下保留生成历史
系统 SHALL 在无存储可用时仍记录生成元数据。

#### Scenario: 无存储时记录元数据
- **WHEN** S3 和本地存储均不可用
- **AND** 客户端 UI 生成完成
- **THEN** 系统 SHALL 创建生成记录，`storage_type = 'upstream'`
- **AND** `media_url` 为上游临时 URL
- **AND** 系统 SHALL 不累加存储配额

#### Scenario: 过期 URL 标记与倒计时
- **WHEN** 生成记录的 `storage_type = 'upstream'`
- **THEN** 客户端 SHALL 显示 15 分钟倒计时进度条（基于 `completed_at` 计算剩余时间）
- **AND** 剩余 5 分钟时 SHALL 通过浏览器通知提醒用户
- **AND** 剩余 2 分钟时卡片边框 SHALL 变为红色警告态
- **AND** 超过 15 分钟后 SHALL 显示"链接已过期，作品无法恢复"，禁用下载和保存按钮
