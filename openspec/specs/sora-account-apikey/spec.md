## ADDED Requirements

### Requirement: Sora 平台支持 API Key 账号类型
系统 SHALL 为 Sora 平台新增 "API Key / 上游透传" 账号类型，取消现有 OAuth 硬编码限制。

#### Scenario: 前端创建 Sora API Key 账号
- **WHEN** 管理员在账号创建对话框中选择 Sora 平台
- **THEN** 系统 SHALL 显示两个账号类别选项卡："OAuth 认证"和"API Key / 上游透传"
- **AND** 选择"API Key / 上游透传"时 SHALL 显示 `Base URL`（必填）和 `API Key`（必填）表单字段
- **AND** 提交时 `form.type` SHALL 设置为 `'apikey'`

#### Scenario: Base URL 字段校验
- **WHEN** 管理员创建或编辑 `platform=sora, type=apikey` 账号
- **THEN** `base_url` SHALL 为必填
- **AND** `base_url` SHALL 以 `http://` 或 `https://` 开头
- **AND** 不满足校验时 SHALL 拒绝保存并提示明确错误

#### Scenario: 取消 Sora OAuth 硬编码
- **WHEN** 用户选择 Sora 平台
- **THEN** 系统 SHALL 不再强制设置 `form.type = 'oauth'`
- **AND** SHALL 允许用户选择 OAuth 或 API Key 类型

### Requirement: Sora API Key 账号编辑
系统 SHALL 支持编辑 Sora API Key 类型账号的 `base_url` 和 `api_key`。

#### Scenario: 编辑 Sora API Key 账号
- **WHEN** 管理员编辑一个 `platform=sora, type=apikey` 的账号
- **THEN** 编辑界面 SHALL 显示 `Base URL` 和 `API Key` 可编辑字段
- **AND** 保存时 SHALL 更新 `credentials` 中的 `base_url` 和 `api_key`

### Requirement: Sora API Key 账号连通性测试
系统 SHALL 支持 Sora API Key 账号的连通性测试。

#### Scenario: 测试连通性成功
- **WHEN** 管理员点击"测试连接"
- **AND** 上游 `base_url` 可达且 `api_key` 有效
- **THEN** 系统 SHALL 发送轻量级请求到上游验证连通性
- **AND** 返回测试成功结果

#### Scenario: 测试连通性失败
- **WHEN** 上游不可达或认证失败
- **THEN** 系统 SHALL 返回明确的错误信息（如"连接超时"、"认证失败"）

### Requirement: Sora apikey 账号 HTTP 透传
系统 SHALL 对 `type=apikey` 的 Sora 账号执行 HTTP 透传，而非 SDK 直连。

#### Scenario: apikey 账号走 HTTP 透传
- **WHEN** `SoraGatewayService.Forward()` 检测到 `account.Type == "apikey"` 且 `account.GetBaseURL() != ""`
- **THEN** 系统 SHALL 调用 `forwardToUpstream()` 方法
- **AND** SHALL 不使用 `SoraSDKClient` 直连

#### Scenario: HTTP 透传请求构造
- **WHEN** 系统执行 `forwardToUpstream()`
- **THEN** 系统 SHALL 构造 HTTP POST 请求到规范化拼接的 `{base_url}/sora/v1/chat/completions`
- **AND** Header SHALL 包含 `Authorization: Bearer <api_key>` 和 `Content-Type: application/json`
- **AND** 请求体 SHALL 原样透传客户端请求体

#### Scenario: 流式响应透传
- **WHEN** 上游返回流式 SSE 响应
- **THEN** 系统 SHALL 逐字节透传 SSE 流到客户端
- **AND** SHALL 不缓存完整响应

#### Scenario: 非流式响应透传
- **WHEN** 上游返回非流式 JSON 响应
- **THEN** 系统 SHALL 读取完整响应后原样返回客户端

#### Scenario: 上游错误触发失败转移
- **WHEN** 上游返回 401/403/429/5xx 错误
- **THEN** 系统 SHALL 复用现有的 `UpstreamFailoverError` 机制触发账号切换

### Requirement: sub2api 二级桥接
系统 SHALL 通过 API Key 账号类型天然支持 sub2api 级联部署。

#### Scenario: 分站通过 API Key 连接总站
- **WHEN** 分站创建 Sora API Key 账号，`base_url` 指向总站地址
- **THEN** 分站的 Sora 请求 SHALL 通过 HTTP 透传到总站的 `/sora/v1/chat/completions`
- **AND** 总站 SHALL 使用自己的 OAuth 账号连接 OpenAI

#### Scenario: 级联中的存储独立性
- **WHEN** 分站收到总站返回的生成结果
- **THEN** 分站 SHALL 根据自己的 S3 配置决定是否存储
- **AND** 存储行为与总站无关（完全独立）
