# sora-generation-gateway Specification

## Purpose
为 Sora 图片/视频生成提供完整接入能力：网关入口、调度与并发、OAuth token 生命周期与一致性、模型列表、计费与用量、媒体回链与前端支持，并保障大包体与长耗时请求。

说明：本系统采用 Sora 直连上游模式（使用 OAuth access_token 调用上游任务接口），不依赖 sora2api；因此本文不包含“sora2api token 池同步”要求。

## Requirements

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
- **THEN** 网关 SHALL 选择“强制流式并聚合”或“返回明确错误”，并在文档中一致说明
- **AND** 默认策略 SHALL 为“强制流式并聚合”

#### Scenario: 非 Sora 分组调用 /v1/chat/completions
- **WHEN** 请求的 API Key 分组平台不为 `sora`
- **THEN** 网关 SHALL 返回 4xx 并提示不支持该平台

### Requirement: Sora 调度与失败切换
系统 SHALL 对 Sora 账号执行调度、并发控制、失败切换，与 OpenAI 调度一致。

#### Scenario: 账号可用时成功调度
- **WHEN** 至少存在一个可调度的 Sora 账号
- **THEN** 选择优先级最高且最近未使用的账号，并在完成后刷新 LRU

#### Scenario: 上游失败触发切换
- **WHEN** 上游返回 401/403/429/5xx
- **THEN** 网关 SHALL 切换账号并重试，直到达到最大切换次数

### Requirement: Sora OAuth Token 生命周期与一致性
系统 SHALL 明确 Sora OAuth 账号的 token 来源、刷新责任与同步策略，避免 refresh_token 轮换导致源/派生不一致。

#### Scenario: 关联 Sora 账号（Derived）不独立刷新
- **GIVEN** 账号 `platform=sora` 且满足 Derived 判定条件（详见下方场景）
- **WHEN** 后台 `token_refresh` 执行刷新周期
- **THEN** 系统 SHALL 跳过对该 Sora 账号的独立刷新
- **AND** 系统 SHALL 不因该账号“未刷新”而将其标记为 `error`

#### Scenario: OpenAI 源账号刷新后同步派生 Sora 账号
- **GIVEN** OpenAI OAuth 账号刷新成功（新 `access_token/refresh_token/expires_at`）
- **WHEN** 系统检测到存在关联的 Derived Sora 账号
- **THEN** 系统 SHALL 将新 token 同步更新到关联的 Sora 账号 `accounts.credentials`
- **AND** 若部署包含 `sora_accounts` 扩展表（migration 046），系统 SHALL 同步更新扩展表字段
- **AND** 系统 SHALL 失效关联 Sora 账号的 OAuth token cache，避免继续命中旧 token
- **AND** 系统 SHALL 同步更新 scheduler cache，保证调度读取到最新 credentials

#### Scenario: 请求路径获取 token 时优先使用源账号
- **GIVEN** 请求被调度到 Derived Sora 账号
- **WHEN** Sora 直连客户端需要获取 access_token
- **THEN** 系统 SHALL 优先使用关联 OpenAI 源账号的 access_token（允许触发源账号刷新）
- **AND** 若派生账号的 `accounts.credentials` / `sora_accounts` 与源账号不一致（access_token/refresh_token/expires_at 任一不同或为空），系统 SHALL 触发统一同步流程进行修复（允许异步执行；若异步，系统 SHALL 记录结构化日志，包含 source_account_id / derived_account_id，并在同步失败时输出告警）
- **AND** 若在请求路径触发了刷新，系统 SHALL 执行与后台刷新一致的同步流程（双表同步 + cache/scheduler cache 一致性）

#### Scenario: 请求路径触发刷新时的 Proxy 选择（Derived）
- **GIVEN** 请求被调度到 Derived Sora 账号，且该请求触发了 token 刷新
- **WHEN** 系统发起 refresh_token 刷新请求
- **THEN** 系统 SHALL 优先使用当前 Sora 账号配置的 Proxy 作为刷新网络出口
- **AND** 若当前 Sora 账号未配置 Proxy，系统 SHALL 回退使用源 OpenAI 账号的 Proxy

#### Scenario: 独立 Sora 账号（Standalone）可刷新
- **GIVEN** 账号被判定为 Standalone Sora
- **WHEN** token 接近过期并触发刷新
- **THEN** 系统 SHALL 允许使用 OpenAI OAuth refresh_token 流程刷新该账号

#### Scenario: Derived 判定条件
- **GIVEN** 账号 `platform=sora` 且 `extra.linked_openai_account_id` 存在
- **WHEN** 系统解析并加载关联账号
- **THEN** 仅当以下条件全部满足才视为 Derived：
  - `linked_openai_account_id` 可解析为正整数
  - 关联账号存在
  - 关联账号 `platform=openai` 且 `type=oauth`
  - 关联账号 `credentials.refresh_token` 非空
- **AND** 若任一条件不满足，系统 SHALL 将该 Sora 账号视为 Standalone

#### Scenario: Standalone 刷新不可用时的明确失败
- **GIVEN** Sora 账号被判定为 Standalone
- **WHEN** 该账号缺失可用的 `credentials.refresh_token` 且 access_token 已过期/即将过期
- **THEN** 系统 SHALL 立即失败并返回明确错误，错误原因 SHALL 包含 `OPENAI_OAUTH_NO_REFRESH_TOKEN`
- **AND** 系统 SHALL 将该账号标记为不可调度（`error`），避免继续被调度
- **AND** 系统 SHALL 不进行静默重试以避免 401/403 抖动与 failover 风暴

### Requirement: 刷新失败不应误伤 Sora 可调度性
系统 SHALL 保证派生 Sora 账号不会因为“刷新策略”而被误标记为不可用。

#### Scenario: Derived Sora 不应因刷新失败进入 error
- **GIVEN** Derived Sora 账号不参与刷新
- **WHEN** token_refresh 周期运行
- **THEN** 系统 SHALL 不对 Derived Sora 执行 SetError
- **AND** Sora 账号的可调度性由常规健康检查/上游错误策略决定

#### Scenario: 关闭后台刷新仍能保持一致性
- **GIVEN** 系统关闭 `token_refresh.enabled`
- **WHEN** 请求路径触发 token 按需刷新
- **THEN** 系统 SHALL 仍执行统一同步流程（双表同步 + cache/scheduler cache 一致性）

### Requirement: 输入格式兼容
系统 SHALL 支持与 OpenAI Chat Completions 兼容的 Sora 输入格式，并兼容 Sora 扩展字段。

#### Scenario: 多模态输入解析
- **WHEN** `messages` 中包含 `image_url` / `video_url`
- **THEN** 网关 SHALL 支持并解析 `image_url` / `video_url`（string 或 object 两种表示）中的 URL 用于生成

#### Scenario: 顶层 image/video 字段
- **WHEN** 请求包含顶层 `image` / `video` / `remix_target_id`
- **THEN** 网关 SHALL 支持并解析顶层 `image` / `video` / `remix_target_id` 字段，并保留其语义
- **AND** 若同时存在顶层 `image`/`video` 与 `messages` 内的 `image_url`/`video_url`，网关 SHALL 以顶层字段为优先

### Requirement: Sora 模型列表
系统 SHALL 为 Sora 分组返回系统内置的 Sora 模型列表（可受配置过滤）。

#### Scenario: 获取 Sora 模型列表
- **WHEN** `GET /v1/models` 且分组平台为 `sora`
- **THEN** 返回系统内置的 Sora 模型集合

#### Scenario: 获取 Sora 模型列表（Sora 专用路由）
- **WHEN** `GET /sora/v1/models`
- **THEN** 返回系统内置的 Sora 模型集合


#### Scenario: prompt-enhance 模型过滤
- **WHEN** 配置关闭 prompt-enhance 模型对用户展示
- **THEN** 网关 SHALL 从模型列表中过滤相关模型


### Requirement: 计费与用量记录
系统 SHALL 记录 Sora 图片/视频的用量与成本。

#### Scenario: 图片生成计费（按次）
- **WHEN** 图片生成完成
- **THEN** 记录 image_count 与 image_size，并根据分组“按次”价格计算成本

#### Scenario: 视频生成计费（按次）
- **WHEN** 视频生成完成
- **THEN** 记录 media_type 与模型档位，并根据分组“按次”价格计算成本

### Requirement: 响应格式易用性
系统 SHALL 提供可解析的输出结果。

#### Scenario: 非流式响应格式
- **WHEN** 系统返回 Markdown 格式内容
- **THEN** 网关 SHOULD 追加结构化 URL 字段（字段名建议为 `media_url`）以提升易用性

### Requirement: Pro/Pro-HD 模型提示
系统 SHALL 对 Pro/Pro-HD 模型不可用情况提供明确提示。

#### Scenario: 非 Pro 账号请求 Pro 模型
- **WHEN** 上游返回 Pro 订阅不足相关错误
- **THEN** 网关 SHALL 返回明确错误信息，避免用户误解

### Requirement: 媒体回链与代理
系统 SHALL 保证用户可访问上游返回的媒体地址。

#### Scenario: 返回 /tmp 或 /static URL
- **WHEN** 上游返回 `/tmp/*` 或 `/static/*` URL
- **THEN** 网关 SHALL 改写为本系统可访问的代理 URL

#### Scenario: 绝对 URL 不改写
- **WHEN** 上游返回可公网访问的绝对 URL
- **THEN** 网关 SHALL 保持原始 URL

### Requirement: 前端 Sora 平台支持
系统 SHALL 在前端展示 Sora 平台与文案。

#### Scenario: 平台选择与展示
- **WHEN** 管理员创建/编辑分组或账号
- **THEN** 前端可选择 `sora` 平台并显示对应图标与 i18n 文案

### Requirement: 大包体与长耗时保障
系统 SHALL 为 Sora 请求配置独立的包体上限与超时时间。

#### Scenario: 大尺寸 Base64 上传
- **WHEN** 请求包含大尺寸图片或视频 base64
- **THEN** 网关 SHALL 在 Sora 路由允许更大的 body size

#### Scenario: 长耗时视频生成
- **WHEN** 视频生成耗时较长
- **THEN** 网关 SHALL 维持流式连接并避免过早超时
