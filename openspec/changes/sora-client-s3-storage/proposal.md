# Sora 客户端功能完善

## Why

当前 Sora 功能存在五个核心问题：

1. **容量瓶颈** — 视频文件单个可达 200MB+，本地磁盘存储会快速耗尽，且默认 7 天清理导致用户资产丢失。
2. **缺乏客户端体验** — 没有面向用户的 Sora 界面，用户只能通过 API 调用，无法像 Sora 官方客户端那样浏览作品、管理任务。
3. **无存储时的体验断裂** — 如果管理员未配置 S3 存储，用户生成完毕后只能拿到一个临时 URL，离开对话后就丢失了。
4. **不支持级联部署** — 当前 Sora 平台账号只支持 OAuth 直连 OpenAI，无法实现 `sub2api(API Key) → sub2api(OAuth) → OpenAI` 的两层桥接架构，限制了多站点分发和权限隔离场景。
5. **账户管理缺少 Sora API Key 类型** — 当前"账户管理 > 创建账号"中 Sora 平台被硬编码为仅支持 OAuth（`CreateAccountModal.vue` 第 2597-2601 行强制设置 `form.type = 'oauth'`）。其他平台如 Anthropic、OpenAI、Gemini 都支持 API Key 类型（可配置 `base_url` + `api_key` 指向自定义上游），Antigravity 还支持 upstream 类型。Sora 缺少这个选项，导致无法实现级联部署。

## What Changes

### 一、存储层改造（管理员配置 S3 供用户使用）

- 在系统设置中新增独立的 Sora S3 存储配置（endpoint、bucket、region、access_key、secret_key 等）
- 管理员配置并启用后，系统将生成的媒体直接通过 `aws-sdk-go-v2` 上传到 S3 兼容存储，返回 CDN/签名 URL 给用户
- 不依赖现有数据管理的 gRPC 代理，S3 配置独立管理
- 保留本地存储作为回退方案（S3 不可用时降级）

### 二、两种调用路径的存储策略差异

系统有两种调用入口，存储行为完全不同：

**路径 A：API Key 直接调用**（`/sora/v1/chat/completions`，开发者/程序调用）

- **不存储媒体** — 生成完成后直接返回上游 URL，由调用方自行下载
- **不记录生成历史** — 不写入 `sora_generations` 表（保持现有行为，纯透传网关）
- **不检查存储配额** — 仅检查余额/计费（现有逻辑）
- **理由**：API 用户是开发者，有能力自行处理媒体下载和存储，不应强制消耗系统 S3 空间

**路径 B：Sora 客户端 UI 调用**（`/api/v1/sora/generate`，Web 界面用户）

- **异步生成** — 立即返回 generation_id，后台异步完成生成（用户无需同步等待 5-20 分钟）
- **自动存储到 S3** — 生成完成后自动上传到管理员配置的 S3，返回永久 URL
- **记录生成历史** — 写入 `sora_generations` 表，用户可在作品库浏览
- **检查存储配额** — 生成前检查，超限拒绝并引导用户释放空间
- **支持取消** — 用户可取消进行中的生成任务
- **理由**：Web 用户需要持久化作品、浏览历史、管理生成记录

**无存储场景的降级**（仅影响路径 B）：

当管理员**未配置 S3 存储**时，路径 B 的行为：

- **生成完成后**：直接将上游临时 URL 透传给客户端
- **客户端提示**：显示醒目提示 —— "存储未配置，请立即下载，链接将在短时间内过期"
- **自动下载触发**：生成完成后自动触发浏览器下载（或提供一键下载按钮）
- **生成记录保留**：仍记录生成元数据（提示词、模型、状态），`storage_type='upstream'`，`media_url` 为上游临时 URL
- **降级链**：`S3 存储（优先）→ 本地磁盘存储（回退）→ 上游临时 URL 透传（最终回退，即生即下载）`

**对照总结**：

```
                      路径 A: API Key 直接调用          路径 B: Sora 客户端 UI
                      /sora/v1/chat/completions       /api/v1/sora/generate
  ─────────────────────────────────────────────────────────────────────────
  使用者               开发者 / 程序                    Web 界面用户
  存储到 S3            ❌ 不存储                        ✅ 上传到 S3
  生成记录             ❌ 不记录                        ✅ 写入 sora_generations
  配额检查             ❌ 不检查存储配额                 ✅ 检查
  返回内容             上游临时 URL（自行下载）          S3 永久 URL（可浏览历史）
  无存储时             正常返回临时 URL（无影响）        降级为即生即下载 + 提示
```

### 三、用户存储配额

- 新增用户级别的 Sora 存储配额字段（默认值由管理员在系统设置中配置）
- 管理员可为单个用户或分组设置不同的存储配额上限
- 每次生成完成并上传后累计用户已用空间，超出配额则拒绝新的生成请求
- 提供配额查询 API，用户可在客户端中查看已用/剩余空间

### 四、账户管理新增 Sora API Key / 上游透传 账号类型

**现状分析**：

当前 Sora 平台在账号创建界面中被硬编码为仅支持 OAuth 类型：

```typescript
// CreateAccountModal.vue 第 2597-2601 行
if (newPlatform === 'sora') {
  accountCategory.value = 'oauth-based'
  addMethod.value = 'oauth'
  form.type = 'oauth'
}
```

而其他平台已支持多种账号类型：

| 平台 | OAuth | Setup Token | API Key | Upstream |
|------|-------|-------------|---------|----------|
| Anthropic | ✅ | ✅ | ✅ (`base_url` + `api_key`) | — |
| OpenAI | ✅ | — | ✅ (`base_url` + `api_key`) | — |
| Gemini | ✅ (3种OAuth方式) | — | ✅ (`base_url` + `api_key`) | — |
| Antigravity | ✅ | — | — | ✅ (实际存为`apikey`类型, `base_url` + `api_key`) |
| **Sora** | **✅ (唯一)** | **—** | **❌ 缺失** | **❌ 缺失** |

**设计方案**：

为 Sora 平台新增 "API Key / 上游透传" 账号类型选项，复用 Antigravity 的 upstream 模式（实际存储为 `apikey` 类型）：

**前端变更**（`CreateAccountModal.vue`）：

- 取消 Sora 平台的硬编码 OAuth 限制
- Sora 平台展示两个账号类别选项卡：
  - **OAuth 认证**（现有功能，连接 OpenAI Sora 官方）
  - **API Key / 上游透传**（新增，连接另一个 sub2api 或兼容 API）
- 选择"API Key / 上游透传"时，显示以下表单字段：
  - `Base URL`（必填）— 上游 Sora 服务地址，默认占位符 `https://your-upstream-sub2api.com`
  - `API Key`（必填）— 上游服务的 API Key，占位符 `sk-...`
- 表单提交时，`form.type = 'apikey'`，credentials 包含 `{ base_url, api_key }`

**前端 UI 交互设计**：

```
┌─ 创建 Sora 账号 ─────────────────────────────────┐
│                                                    │
│  平台: [Sora ▼]                                    │
│                                                    │
│  账号类型:                                          │
│  ┌──────────────┐  ┌──────────────────────┐        │
│  │ ● OAuth 认证  │  │ ○ API Key / 上游透传  │        │
│  └──────────────┘  └──────────────────────┘        │
│                                                    │
│  ── 选择 OAuth 认证时（现有流程）──                   │
│  [发起 OpenAI OAuth 授权]                           │
│                                                    │
│  ── 选择 API Key / 上游透传时（新增）──               │
│  Base URL:  [https://upstream.example.com     ]    │
│  API Key:   [sk-••••••••••••••••              ]    │
│                                                    │
│  提示: 适用于连接另一个 sub2api 实例或兼容的          │
│        Sora API 服务。请求将以 API Key 认证          │
│        透传到上游的 /sora/v1/chat/completions        │
│                                                    │
│              [测试连接]    [创建账号]                 │
└────────────────────────────────────────────────────┘
```

**后端变更**：

- 后端验证逻辑无需修改（已允许 `oneof=oauth setup-token apikey upstream`）
- `SoraGatewayService.Forward()` 新增分支：当 `account.Type == "apikey"` 且 `account.GetBaseURL() != ""` 时，不走 `SoraSDKClient`，而是将请求 HTTP 透传到规范化后的上游地址（`{base_url}/sora/v1/chat/completions`），Header 中携带 `Authorization: Bearer <api_key>`
- Sora apikey 账号创建/编辑时强制校验 `base_url`（必填，需包含 `http://` 或 `https://` scheme）
- 复用现有 `Account.GetBaseURL()` 方法（已支持 `apikey` 类型返回 `base_url`）
- 响应直接透传回客户端（流式/非流式均兼容）

**编辑账号**（`EditAccountModal.vue`）：

- 现有 Sora OAuth 账号的编辑功能不变
- 新增 API Key 类型 Sora 账号的编辑支持（可修改 `base_url` 和 `api_key`）
- 账号测试（`AccountTestModal.vue`）：API Key 类型发送一个轻量级请求到上游验证连通性

### 五、sub2api 二级桥接（基于第四节的 API Key 账号类型实现）

**场景**：分站 A（面向终端用户）需要通过总站 B（拥有 Sora OAuth 账号）来访问 OpenAI Sora。

```
终端用户 → sub2api-A(分站) → sub2api-B(总站) → OpenAI Sora
              │                    │
              │ Sora apikey 账号    │ Sora oauth 账号
              │ base_url=总站地址    │ access_token=OpenAI
              │ api_key=总站Key     │
              ▼                    ▼
         HTTP 透传请求          SDK 直连 OpenAI
```

**实现方式**：

级联部署不需要额外的代码能力，完全基于第四节新增的 "API Key / 上游透传" 账号类型：

1. **总站 B**：创建 Sora OAuth 账号（现有功能），正常连接 OpenAI
2. **总站 B**：创建 API Key（`/sora` 端点的 Key），供分站使用
3. **分站 A**：创建 Sora API Key 账号（第四节新增），填写：
   - `base_url` = 总站 B 的地址（如 `https://main-site.example.com`）
   - `api_key` = 总站 B 发放的 API Key
4. **分站 A** 的 Sora 网关检测到 `apikey` 类型账号，将请求透传到总站 B 的 `/sora/v1/chat/completions`
5. **总站 B** 收到请求后用 OAuth 账号走 SDK 连接 OpenAI，返回结果
6. **分站 A** 收到响应后，由自己决定是否存储到 S3（存储层完全独立）

**为什么需要级联**：

- **权限隔离**：OAuth 账号是敏感资产，集中管理在总站，分站只需 API Key
- **多站点分发**：一个总站可服务多个分站，每个分站独立管理用户和计费
- **运维简化**：OAuth Token 刷新、Cloudflare 防护等复杂逻辑只需总站处理

**注意**：级联不是独立的能力，而是 `sora-upstream-bridge` 能力的一个使用场景。

### 六、Sora 客户端界面（参考官方客户端）

- 在前端新增 Sora 客户端页面（`/sora`），面向普通用户
- **嵌入全局布局**：Sora 页面保留在全局侧边栏布局内渲染（不独立接管全屏），页面内仅保留 Tab 切换 + 配额进度条，去掉独立 Logo 和头像（由侧边栏提供）
- **条件显示**：侧边栏 Sora 菜单项仅在管理员配置了活跃 Sora 账号时显示（`sora_client_enabled`）
- **生成页面**：输入提示词、选择模型（视频/图片/分辨率/时长）、上传参考图、发起生成
- **作品库页面**：网格展示历史生成作品（缩略图/视频预览），支持下载、删除
- **生成进度**：实时显示当前生成任务的进度状态（排队中/生成中/完成/失败）
- **配额展示**：展示当前用户的存储用量和剩余配额
- **无存储提醒**：未配置存储时，显示即时下载提示和自动下载功能

### 七、后端 Sora 生成记录

- 新增 `sora_generations` 表，记录每次生成的元数据（用户、模型、提示词、媒体 URL、文件大小、状态、存储方式等）
- 提供生成记录的 CRUD API（列表、详情、删除）
- 删除作品时同步清理 S3 中的文件并释放配额
- 无存储模式下仍记录生成历史（但媒体 URL 会标记过期）

### 八、管理员配置

- 在系统设置中新增独立的 Sora S3 存储配置（endpoint、bucket、region、access_key_id、secret_access_key、prefix、force_path_style 等），使用 `aws-sdk-go-v2` 直连
- 在系统设置中新增 Sora 默认存储配额配置
- 在用户管理 / 分组管理中可覆盖单个用户 / 分组的配额

## Capabilities

### New Capabilities

- `sora-s3-media-storage`: 将 Sora 生成的媒体文件上传到管理员在系统设置中配置的 S3 兼容存储（S3/R2/OSS/MinIO），使用 `aws-sdk-go-v2` 直连，替代本地磁盘存储，支持 CDN/签名 URL 分发。无存储时降级为即生即下载模式。

- `sora-user-storage-quota`: 用户级别的 Sora 存储配额管理，包括配额设置（系统默认值 + 用户/分组覆盖）、用量追踪（每次上传累计）、超限拒绝、配额查询 API。

- `sora-generation-history`: Sora 生成记录的持久化存储与管理，包括 `sora_generations` 表、CRUD API、作品删除与 S3 文件清理联动、无存储模式下的历史保留与过期标记。

- `sora-client-ui`: 面向用户的 Sora 客户端前端界面，参考 Sora 官方客户端设计，包括生成页面（提示词输入、模型选择、参考图上传、多任务并发展示）、作品库（网格展示、下载、删除）、异步生成+前端轮询、自动保存到 S3、取消生成、生成完成浏览器通知、无存储时 15 分钟倒计时提醒、页面刷新后任务恢复。

- `sora-account-apikey`: 为 Sora 平台新增 "API Key / 上游透传" 账号类型。前端：取消 Sora 平台的 OAuth 硬编码限制，新增 API Key 选项卡（`base_url` + `api_key` 表单）；后端：`Forward()` 中检测 `apikey` 类型账号时走 HTTP 透传而非 SDK 直连，请求发到 `base_url/sora/v1/chat/completions`。该能力同时实现了 sub2api 二级桥接（分站 API Key → 总站 OAuth → OpenAI）。

### Modified Capabilities

- `sora-generation-gateway`: 现有 Sora 网关转发逻辑保持不变 — `/sora/v1/chat/completions` 继续作为纯透传网关，不存储媒体、不记录历史、不检查存储配额（仅保留现有的计费和并发控制）。存储/历史/配额逻辑全部由新的 `sora-client-ui`（`/api/v1/sora/generate`）在上层处理。注：apikey 账号的 HTTP 透传由 `sora-account-apikey` 能力负责。

- `sora-s3-settings`: 系统设置新增独立的 Sora S3 存储配置区域（不依赖数据管理的 gRPC S3 Profile），包含完整的 S3 连接参数和测试连接功能。

## Impact

### 数据库变更

- 新增 `sora_generations` 表（生成记录：用户ID、模型、提示词、媒体URL、文件大小、存储方式、`s3_object_keys` JSONB 数组、状态、创建时间）
- 在 `users` 表新增 `sora_storage_quota_bytes`、`sora_storage_used_bytes` 字段（配额上限、已用空间）
- 系统设置新增 Sora S3 配置键值（`sora_s3_endpoint`、`sora_s3_bucket`、`sora_s3_region` 等）
- 系统设置新增 `sora_default_storage_quota_bytes` 键值
- 公共设置 API 新增 `sora_client_enabled` 字段（后端根据活跃 Sora 账号数推断，供前端条件显示 Sora 菜单项）
- 分组表新增 `sora_storage_quota_bytes` 字段（可选覆盖）

### 后端代码变更

- `service/sora_gateway_service.go` — `Forward()` 方法新增 apikey 账号 HTTP 透传分支，并将 API Key 直调路径保持为“不落盘、不记录”的纯透传语义
- `service/sora_s3_storage.go` — 新增 S3 上传能力（使用 `aws-sdk-go-v2` 直连，读取系统设置中的 S3 配置）
- 新增 `service/sora_generation_service.go` — 生成记录 CRUD + S3 文件清理
- 新增 `service/sora_quota_service.go` — 配额管理
- 新增 `service/sora_upstream_forwarder.go` — apikey 类型 Sora 账号的 HTTP 透传逻辑（请求转发 + 流式响应代理）
- 新增 `handler/sora_client_handler.go` — 用户端 Sora API（异步生成、历史、配额、取消、手动保存、存储状态查询）
- `handler/admin/setting_handler.go` — 系统设置新增 Sora S3 配置接口
- `server/routes/` — 新增用户端 Sora 路由

### 前端代码变更

- `components/account/CreateAccountModal.vue` — **核心变更**：取消 Sora 平台 OAuth 硬编码限制，新增"API Key / 上游透传"选项卡和表单（`base_url` + `api_key`）
- `components/account/EditAccountModal.vue` — 支持编辑 Sora apikey 类型账号的 `base_url` 和 `api_key`
- `components/account/credentialsBuilder.ts` — 新增 Sora apikey 类型的 credentials 构建逻辑
- 新增 `views/user/SoraView.vue` — Sora 客户端主页面
- 新增 `components/sora/` — 生成表单、作品库、进度条、配额展示、即时下载等组件
- `api/` — 新增 Sora 客户端 API 调用
- `router/index.ts` — 新增 `/sora` 路由
- `components/layout/AppSidebar.vue` — 新增 Sora 菜单项
- `i18n/` — 新增国际化文本（含 Sora API Key 账号相关翻译）
- 管理端"系统设置"页面 — 新增 Sora S3 存储配置区域

### API 变更

- 新增 Sora 客户端 API（路径 B，供 Web UI 使用）：
  - `POST /api/v1/sora/generate` — 发起生成（异步：立即返回 generation_id，后台完成生成 + 自动 S3 上传）
  - `GET /api/v1/sora/generations` — 生成历史列表（支持按状态筛选，用于恢复进行中任务）
  - `GET /api/v1/sora/generations/:id` — 生成详情（前端轮询获取状态更新）
  - `POST /api/v1/sora/generations/:id/save` — 手动保存到存储（仅针对 storage_type='upstream' 的记录，S3 后续启用时使用）
  - `POST /api/v1/sora/generations/:id/cancel` — 取消进行中的生成任务
  - `DELETE /api/v1/sora/generations/:id` — 删除作品（联动 S3 清理 + 释放配额）
  - `GET /api/v1/sora/quota` — 查询配额用量
  - `GET /api/v1/sora/models` — 可用模型列表
  - `GET /api/v1/sora/storage-status` — 存储状态查询（S3 是否可用，供前端决定 UI 展示）
- 现有 API（路径 A，不变）：
  - `/sora/v1/chat/completions` — 保持纯透传，不存储、不记录，直接返回 URL 给 API 调用方自行下载
- 扩展管理端 API：系统设置新增 Sora S3 配置读写接口

### 依赖

- 需要 S3 兼容的 Go SDK（`github.com/aws/aws-sdk-go-v2`），直连 S3 存储
- 现有 `go-sora2api v1.1.0` SDK 无需变更

## 现有架构参考（代码分析摘要）

### 当前 Sora 网关转发流程

```
POST /sora/v1/chat/completions
  │
  ├─ 并发控制（用户级 + 账号级）
  ├─ 账号选择 + 失败转移（最多切 3 个账号）
  │
  ▼
SoraGatewayService.Forward()
  ├─ 解析请求（模型、提示词、图片/视频输入）
  ├─ 预检查（PreflightCheck 验证额度）
  ├─ 创建任务（CreateImage/Video/StoryboardTask）
  ├─ 轮询等待完成（poll 2s 间隔，最多 600 次 = 20 分钟）
  ├─ [可选] 去水印处理
  ├─ 下载到本地存储 / 回退上游 URL
  └─ 返回 Chat Completions 格式响应
```

### Sora S3 存储方案

- 独立于现有数据管理的 gRPC S3 Profile 体系
- 在系统设置（Settings 表）中新增 Sora S3 配置项
- 后端使用 `aws-sdk-go-v2` 直接连接 S3 兼容存储
- 前端在系统设置页面新增 Sora S3 配置区域（含测试连接按钮）

### 现有账号类型（各平台支持情况）

| Type | 说明 | Anthropic | OpenAI | Gemini | Antigravity | Sora (现状) | Sora (本次新增) |
|------|------|-----------|--------|--------|-------------|-------------|----------------|
| `oauth` | OAuth 认证 | ✅ | ✅ | ✅ (3种方式) | ✅ | ✅ 唯一支持 | ✅ 保留 |
| `setup-token` | Setup Token | ✅ | — | — | — | ❌ | — |
| `apikey` | API Key + base_url | ✅ | ✅ | ✅ | ✅ (作为upstream) | ❌ | **✅ 新增** |
| `upstream` | 上游透传 | — | — | — | — | ❌ | — |

**关键限制代码**（本次需修改）：
- `CreateAccountModal.vue:2597-2601` — Sora 平台强制 `form.type = 'oauth'`
- `Account.GetBaseURL()` — 仅 `apikey` 类型返回 `base_url`（无需修改，已兼容）
- `handler/admin/account_handler.go:94` — 验证已支持 `apikey`（无需修改）

### 关键文件路径

| 模块 | 文件 |
|------|------|
| **Sora 后端** | |
| Sora 网关服务 | `service/sora_gateway_service.go` (1484 行) |
| Sora SDK 客户端 | `service/sora_sdk_client.go` |
| Sora 媒体存储 | `service/sora_media_storage.go` |
| Sora 模型配置 | `service/sora_models.go` |
| Sora HTTP Handler | `handler/sora_gateway_handler.go` |
| Sora 路由注册 | `server/routes/gateway.go` (第 104-124 行) |
| **账号管理后端** | |
| 账号类型常量 | `domain/constants.go` |
| 账号 Service | `service/account.go` (`GetBaseURL()` 第 521 行) |
| 账号 Handler | `handler/admin/account_handler.go` (验证规则第 94 行) |
| **账号管理前端** | |
| 创建账号对话框 | `components/account/CreateAccountModal.vue` (2100+ 行, **Sora 硬编码在第 2597 行**) |
| 编辑账号对话框 | `components/account/EditAccountModal.vue` |
| Credentials 构建 | `components/account/credentialsBuilder.ts` |
| **系统设置** | |
| 系统设置 Handler | `handler/admin/setting_handler.go` |
| 前端系统设置页 | `views/admin/SettingsView.vue`（新增 Sora S3 配置区域） |
| **其他前端** | |
| 前端路由 | `router/index.ts` |
| 前端侧边栏 | `components/layout/AppSidebar.vue` |
| 前端类型定义 | `types/index.ts` (`AccountPlatform`, `AccountType` 第 485 行) |
