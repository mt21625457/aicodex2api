## ADDED Requirements

### Requirement: Sora S3 存储配置
系统 SHALL 在系统设置中提供独立的 Sora S3 存储配置，使用 `aws-sdk-go-v2` 直连 S3 兼容存储，不依赖现有数据管理的 gRPC 代理。

#### Scenario: 系统设置新增 Sora S3 配置项
- **WHEN** 管理员访问系统设置页面
- **THEN** 页面 SHALL 显示"Sora S3 存储配置"区域
- **AND** 包含以下配置项：
  - 启用开关（`sora_s3_enabled`）
  - S3 端点（`sora_s3_endpoint`）
  - 区域（`sora_s3_region`）
  - 存储桶（`sora_s3_bucket`）
  - 访问密钥 ID（`sora_s3_access_key_id`）
  - 访问密钥（`sora_s3_secret_access_key`，加密存储，界面显示为密码框）
  - 对象键前缀（`sora_s3_prefix`，可选）
  - 强制路径模式（`sora_s3_force_path_style`，可选）
  - CDN 域名（`sora_s3_cdn_url`，可选）

#### Scenario: 保存 Sora S3 配置
- **WHEN** 管理员填写 S3 配置并点击保存
- **THEN** 系统 SHALL 将配置保存到 Settings 表
- **AND** `sora_s3_secret_access_key` SHALL 加密存储
- **AND** Sora S3 Storage Service SHALL 刷新缓存的 S3 客户端配置

#### Scenario: 测试 S3 连接
- **WHEN** 管理员点击"测试连接"按钮
- **THEN** 系统 SHALL 使用当前表单中的配置创建临时 S3 客户端
- **AND** 执行 `HeadBucket` 或 `PutObject` + `DeleteObject` 测试连通性
- **AND** 返回测试结果（成功/失败 + 错误信息）

#### Scenario: 禁用 Sora S3 存储
- **WHEN** 管理员关闭 `sora_s3_enabled` 开关
- **THEN** Sora 客户端 UI 的生成结果 SHALL 降级到本地存储或上游 URL 透传

#### Scenario: S3 配置不完整
- **WHEN** `sora_s3_enabled = true` 但缺少必填字段（endpoint/bucket/access_key_id/secret_access_key）
- **THEN** 系统 SHALL 视为 S3 存储不可用
- **AND** SHALL 在日志中记录配置不完整的警告
