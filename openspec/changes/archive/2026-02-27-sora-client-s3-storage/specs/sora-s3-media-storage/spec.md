## ADDED Requirements

### Requirement: S3 媒体存储服务初始化
系统 SHALL 在启动时从系统设置（Settings 表）读取 Sora S3 配置，使用 `aws-sdk-go-v2` 初始化 S3 客户端。

#### Scenario: Sora S3 已启用且配置完整
- **WHEN** 系统启动或 S3 配置变更
- **AND** Settings 中 `sora_s3_enabled = true` 且必填字段（endpoint、bucket、access_key_id、secret_access_key）均已配置
- **THEN** 系统 SHALL 使用 `aws-sdk-go-v2` 初始化 S3 客户端
- **AND** 系统 SHALL 缓存 S3 客户端实例，标记 S3 存储为可用

#### Scenario: Sora S3 未启用或配置不完整
- **WHEN** 系统启动或 S3 配置变更
- **AND** `sora_s3_enabled = false` 或缺少必填配置
- **THEN** 系统 SHALL 标记 S3 存储为不可用
- **AND** 客户端 UI 调用路径 SHALL 降级为本地存储或即生即下载模式

### Requirement: 媒体文件上传到 S3
系统 SHALL 将 Sora 客户端 UI 生成的媒体文件流式上传到 S3 兼容存储。

#### Scenario: 视频文件上传成功
- **WHEN** Sora 客户端 UI 调用路径生成完成，返回上游媒体 URL
- **AND** S3 存储可用
- **THEN** 系统 SHALL 使用流式管道（`io.Pipe`）从上游 URL 下载并同时上传到 S3
- **AND** S3 object key 格式 SHALL 为 `sora/{user_id}/{YYYY/MM/DD}/{uuid}.{ext}`
- **AND** 上传完成后 SHALL 返回 S3 访问 URL（签名 URL 或 CDN URL）
- **AND** 系统 SHALL 记录 `s3_object_keys` 数组到生成记录中（视频为单元素数组）

#### Scenario: 图片文件上传成功
- **WHEN** Sora 客户端 UI 生成图片完成
- **AND** S3 存储可用
- **THEN** 系统 SHALL 使用与视频相同的上传流程将图片上传到 S3
- **AND** 支持多图场景（`media_urls` 数组中每个 URL 都上传）

#### Scenario: S3 上传失败降级
- **WHEN** S3 上传过程中发生错误（网络超时、权限错误等）
- **THEN** 系统 SHALL 降级到本地磁盘存储（复用现有 `SoraMediaStorage`）
- **AND** 若本地存储也失败，SHALL 降级为返回上游临时 URL
- **AND** 生成记录的 `storage_type` SHALL 反映实际存储位置

#### Scenario: 大文件流式上传避免内存溢出
- **WHEN** 上游媒体文件大于 50MB
- **THEN** 系统 SHALL 使用流式管道上传，不将完整文件缓存到内存
- **AND** 内存峰值 SHALL 不超过 16MB 缓冲区

### Requirement: S3 文件删除
系统 SHALL 在用户删除生成记录时同步删除 S3 中对应的文件。

#### Scenario: 删除 S3 文件（含多图）
- **WHEN** 用户通过作品库删除一条生成记录
- **AND** 该记录的 `storage_type = 's3'` 且 `s3_object_keys` 非空
- **THEN** 系统 SHALL 遍历 `s3_object_keys` 数组，逐一调用 S3 DeleteObject 删除所有文件
- **AND** 释放对应的存储配额（`sora_storage_used_bytes` 减去 `file_size_bytes`）

#### Scenario: S3 删除失败不阻塞记录删除
- **WHEN** S3 DeleteObject 调用失败（部分或全部）
- **THEN** 系统 SHALL 仍然删除数据库中的生成记录
- **AND** 系统 SHALL 记录告警日志，包含失败的 `s3_object_keys` 以便后续清理

### Requirement: 三层降级链
系统 SHALL 支持 S3 → 本地磁盘 → 上游临时 URL 的三层存储降级。

#### Scenario: S3 可用时优先使用 S3
- **WHEN** 客户端 UI 生成完成
- **AND** S3 存储可用
- **THEN** 系统 SHALL 使用 S3 存储，`storage_type = 's3'`

#### Scenario: S3 不可用时降级到本地
- **WHEN** 客户端 UI 生成完成
- **AND** S3 存储不可用但本地存储启用
- **THEN** 系统 SHALL 使用本地存储，`storage_type = 'local'`

#### Scenario: 均不可用时透传上游 URL
- **WHEN** 客户端 UI 生成完成
- **AND** S3 和本地存储均不可用
- **THEN** 系统 SHALL 直接返回上游临时 URL，`storage_type = 'upstream'`
- **AND** 客户端 SHALL 显示即时下载提示

### Requirement: S3 访问 URL 生成策略
系统 SHALL 为 S3 中的媒体文件按配置生成可访问 URL（CDN 优先，预签名兜底）。

#### Scenario: 配置 CDN URL 时返回 CDN 地址
- **WHEN** 系统设置中配置了 `sora_s3_cdn_url`
- **THEN** 系统 SHALL 返回基于 `sora_s3_cdn_url + object_key` 的访问地址
- **AND** SHALL 不额外生成预签名 URL

#### Scenario: 未配置 CDN URL 时生成预签名 URL
- **WHEN** 系统未配置 `sora_s3_cdn_url`
- **THEN** 系统 SHALL 生成 S3 预签名 URL，有效期 SHALL 为 24 小时
- **AND** URL SHALL 支持直接在浏览器中播放/查看

### Requirement: 预签名 URL 动态刷新
系统 SHALL 在返回 S3 媒体记录时动态生成访问 URL，避免预签名过期导致作品库碎图。

#### Scenario: 列表 API 动态生成 URL
- **WHEN** `GET /api/v1/sora/generations` 返回 `storage_type = 's3'` 的记录
- **AND** 未配置 CDN URL
- **THEN** 后端 SHALL 为每条记录的 `s3_object_keys` 动态生成新的预签名 URL 填充到 `media_url` / `media_urls`
- **AND** 前端 SHALL 不缓存这些 URL

#### Scenario: 详情 API 动态生成 URL
- **WHEN** `GET /api/v1/sora/generations/:id` 返回 `storage_type = 's3'` 的记录
- **THEN** 后端 SHALL 动态生成预签名 URL
- **AND** 批量签名性能 SHALL 不影响列表加载速度（使用并发签名或缓存短期 URL）
