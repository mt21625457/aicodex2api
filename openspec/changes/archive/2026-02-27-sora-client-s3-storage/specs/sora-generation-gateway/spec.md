## MODIFIED Requirements

### Requirement: Sora 生成网关入口
系统 SHALL 提供 `POST /v1/chat/completions` 作为 Sora 生成入口（仅限 `platform=sora` 分组）。

#### Scenario: Sora 分组调用 /v1/chat/completions
- **WHEN** 请求的 API Key 分组平台为 `sora`
- **AND** 请求体包含 `model` 与 `messages`
- **THEN** 网关按 Sora 规则处理并返回流式或非流式结果
- **AND** 若生成需要流式，网关 SHALL 强制 `stream=true` 或返回明确提示

#### Scenario: Sora 专用路由调用 /sora/v1/chat/completions
- **WHEN** 客户端请求 `POST /sora/v1/chat/completions`
- **THEN** 网关 SHALL 强制使用 `platform=sora` 的调度与生成逻辑

#### Scenario: 非流式请求策略
- **WHEN** 客户端请求 `stream=false`
- **THEN** 网关 SHALL 选择"强制流式并聚合"或"返回明确错误"，并在文档中一致说明
- **AND** 默认策略 SHALL 为"强制流式并聚合"

#### Scenario: 非 Sora 分组调用 /v1/chat/completions
- **WHEN** 请求的 API Key 分组平台不为 `sora`
- **THEN** 网关 SHALL 返回 4xx 并提示不支持该平台

#### Scenario: API Key 直接调用不存储不记录
- **WHEN** 请求通过 `/sora/v1/chat/completions`（API Key 直接调用路径）
- **THEN** 网关 SHALL 不将媒体文件上传到 S3
- **AND** SHALL 不执行本地磁盘媒体落盘
- **AND** SHALL 不写入 `sora_generations` 表
- **AND** SHALL 不检查存储配额
- **AND** SHALL 直接返回上游 URL（保持现有行为）

### Requirement: Sora 调度与失败切换
系统 SHALL 对 Sora 账号执行调度、并发控制、失败切换，与 OpenAI 调度一致。

#### Scenario: 账号可用时成功调度
- **WHEN** 至少存在一个可调度的 Sora 账号
- **THEN** 选择优先级最高且最近未使用的账号，并在完成后刷新 LRU

#### Scenario: 上游失败触发切换
- **WHEN** 上游返回 401/403/429/5xx
- **THEN** 网关 SHALL 切换账号并重试，直到达到最大切换次数

#### Scenario: apikey 类型账号调度到 HTTP 透传
- **WHEN** 调度选中的 Sora 账号 `type = 'apikey'` 且 `base_url` 非空
- **THEN** 网关 SHALL 调用 `forwardToUpstream()` 执行 HTTP 透传
- **AND** SHALL 不使用 `SoraSDKClient` 直连

## ADDED Requirements

### Requirement: Sora 客户端生成入口
系统 SHALL 提供 `POST /api/v1/sora/generate` 作为客户端 UI 专用生成入口。

#### Scenario: 客户端 UI 调用生成
- **WHEN** 用户通过 Sora 客户端 UI 发起生成请求
- **THEN** 系统 SHALL 接受请求并内部调用现有 `SoraGatewayService.Forward()` 完成生成
- **AND** 在上层包装存储/记录/配额逻辑

#### Scenario: 客户端生成流程（异步）
- **WHEN** `POST /api/v1/sora/generate` 收到请求
- **THEN** 系统 SHALL 按以下顺序执行：
  1. 检查存储配额（有效配额 > 0 时）
  2. 检查用户当前 pending+generating 任务数不超过 3
  3. 创建 `sora_generations` 记录（status=pending）
  4. **立即返回** `{ generation_id, status: "pending" }` 给前端
  5. 后台异步：内部调用 `SoraGatewayService.Forward()` 获取上游媒体 URL（不在该步骤落盘）
  6. 后台异步：自动上传媒体到 S3（若可用），否则降级到本地/上游 URL
  7. 后台异步：更新生成记录（status、media_url、storage_type、file_size 等）
  8. 后台异步：累加存储配额（仅 S3/本地存储时）

#### Scenario: 前端轮询生成状态
- **WHEN** 前端需要获取生成任务最新状态
- **THEN** 系统 SHALL 通过 `GET /api/v1/sora/generations/:id` 返回完整记录
- **AND** 前端 SHALL 按递减频率轮询（3s → 10s → 30s）

#### Scenario: 并发生成上限
- **WHEN** 用户 pending+generating 状态的任务已达 3 个
- **THEN** 系统 SHALL 返回 HTTP 429 + "请等待当前任务完成后再发起新任务"

### Requirement: Sora 可用模型列表 API
系统 SHALL 提供 `GET /api/v1/sora/models` 供客户端 UI 获取可用模型。

#### Scenario: 获取可用 Sora 模型
- **WHEN** 用户请求 `GET /api/v1/sora/models`
- **THEN** 系统 SHALL 返回系统内置的 Sora 模型列表
- **AND** 每个模型 SHALL 包含 `id`、`name`、`media_type`（video/image）、`description`

### Requirement: 手动保存到存储
系统 SHALL 提供 `POST /api/v1/sora/generations/:id/save` 供用户将未自动保存的作品手动上传到 S3。

#### Scenario: 手动保存 upstream 记录到 S3
- **WHEN** 用户请求 `POST /api/v1/sora/generations/:id/save`
- **AND** 该记录 `storage_type = 'upstream'` 且 `media_url` 未过期
- **AND** S3 存储当前可用
- **THEN** 系统 SHALL 从 `media_url` 下载媒体并上传到 S3
- **AND** 更新记录 `storage_type = 's3'`、`s3_object_keys`、`file_size_bytes`
- **AND** 累加用户存储配额

#### Scenario: 手动保存时 URL 已过期
- **WHEN** 上游 URL 已过期（下载返回 403/404）
- **THEN** 系统 SHALL 返回 HTTP 410 + "媒体链接已过期，无法保存"

#### Scenario: 手动保存时 S3 不可用
- **WHEN** S3 存储未启用或配置不完整
- **THEN** 系统 SHALL 返回 HTTP 503 + "云存储未配置，请联系管理员"

### Requirement: 取消生成任务
系统 SHALL 提供 `POST /api/v1/sora/generations/:id/cancel` 供用户取消进行中的生成任务。

#### Scenario: 取消 pending/generating 状态的任务
- **WHEN** 用户请求 `POST /api/v1/sora/generations/:id/cancel`
- **AND** 该记录 `status` 为 `pending` 或 `generating`
- **THEN** 系统 SHALL 将记录状态更新为 `cancelled`
- **AND** SHALL 不累加任何存储配额
- **AND** 若上游任务已提交，后续返回的结果 SHALL 被忽略

#### Scenario: 取消非活跃状态的任务
- **WHEN** 该记录 `status` 为 `completed`、`failed` 或 `cancelled`
- **THEN** 系统 SHALL 返回 HTTP 409 + "任务已结束，无法取消"

### Requirement: 存储状态查询
系统 SHALL 提供 `GET /api/v1/sora/storage-status` 供前端查询当前存储可用性。

#### Scenario: 查询存储状态
- **WHEN** 用户请求 `GET /api/v1/sora/storage-status`
- **THEN** 系统 SHALL 返回 `{ s3_enabled, s3_healthy, local_enabled }`
- **AND** `s3_enabled` 表示管理员是否启用 S3
- **AND** `s3_healthy` 表示 S3 客户端是否初始化成功
- **AND** `local_enabled` 表示本地存储是否可用
