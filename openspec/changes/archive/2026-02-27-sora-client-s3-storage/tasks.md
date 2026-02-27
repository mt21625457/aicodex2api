## 1. 数据库迁移

- [x] 1.1 创建 `sora_generations` 表迁移脚本（含 `s3_object_keys JSONB` 数组字段、所有索引、外键约束）
- [x] 1.2 `users` 表新增 `sora_storage_quota_bytes` 和 `sora_storage_used_bytes` 字段
- [x] 1.3 `groups` 表新增 `sora_storage_quota_bytes` 字段
- [x] 1.4 系统设置新增 `sora_default_storage_quota_bytes` 键值

## 2. Sora S3 存储配置（系统设置）

- [x] 2.1 后端：Settings 表新增 Sora S3 配置键值（sora_s3_enabled、sora_s3_endpoint、sora_s3_region、sora_s3_bucket、sora_s3_access_key_id、sora_s3_secret_access_key、sora_s3_prefix、sora_s3_force_path_style、sora_s3_cdn_url）
- [x] 2.2 后端：系统设置 API 新增 Sora S3 配置读写接口（含 secret_access_key 加密存储）
- [x] 2.3 后端：新增 Sora S3 连接测试接口（HeadBucket 验证连通性）
- [x] 2.4 前端：系统设置页面新增"Sora S3 存储配置"区域（启用开关 + S3 连接表单 + 测试连接按钮）

## 3. Sora API Key 账号类型（sora-account-apikey）

- [x] 3.1 前端 `CreateAccountModal.vue`：取消 Sora 平台 OAuth 硬编码限制（第 2597-2601 行）
- [x] 3.2 前端 `CreateAccountModal.vue`：新增 Sora 平台的"API Key / 上游透传"选项卡和表单（base_url + api_key）
- [x] 3.3 前端 `EditAccountModal.vue`：支持编辑 Sora apikey 类型账号
- [x] 3.4 前端 `credentialsBuilder.ts`：新增 Sora apikey 类型的 credentials 构建逻辑
- [x] 3.5 后端 `sora_gateway_service.go`：`Forward()` 方法新增 apikey 类型分支判断
- [x] 3.6 后端新增 `sora_upstream_forwarder.go`：实现 `forwardToUpstream()` HTTP 透传方法（流式+非流式）
- [x] 3.7 后端 apikey 透传错误处理：复用 `UpstreamFailoverError` 机制实现失败转移
- [x] 3.8 前端/后端：Sora apikey 账号连通性测试支持
- [x] 3.9 前端/后端：Sora apikey 账号 `base_url` 校验（必填 + scheme 合法）与上游 URL 规范化拼接

## 4. S3 媒体存储服务（sora-s3-media-storage）

- [x] 4.1 引入 `aws-sdk-go-v2` 依赖；新增 `service/sora_s3_storage.go`：从 Settings 表读取 S3 配置，初始化 aws-sdk-go-v2 S3 客户端并缓存
- [x] 4.2 实现流式上传方法：从上游 URL 下载并通过 `io.Pipe` 流式上传到 S3
- [x] 4.3 实现 S3 object key 命名规则：`sora/{user_id}/{YYYY/MM/DD}/{uuid}.{ext}`，多图生成多个 key 存入 `s3_object_keys` JSONB 数组
- [x] 4.4 实现 S3 访问 URL 策略：CDN URL 优先，否则动态生成 24h 预签名 URL（列表/详情接口每次请求时重新签名）
- [x] 4.5 实现 S3 文件删除方法（遍历 `s3_object_keys` 数组逐一删除）
- [x] 4.6 实现三层降级链逻辑：S3 → 本地（复用 SoraMediaStorage）→ 上游临时 URL
- [x] 4.7 系统设置中 S3 配置变更时自动刷新缓存的 S3 客户端

## 5. 用户存储配额管理（sora-user-storage-quota）

- [x] 5.1 新增 `service/sora_quota_service.go`：配额优先级判断逻辑（用户 → 分组 → 系统默认）
- [x] 5.2 实现配额检查方法：生成前检查存储是否超限
- [x] 5.3 实现配额原子更新：上传成功后累加用量，删除后释放用量
- [x] 5.4 实现配额查询 API：`GET /api/v1/sora/quota` 返回配额信息

## 6. 生成记录管理（sora-generation-history）

- [x] 6.1 新增 `service/sora_generation_service.go`：生成记录 CRUD 方法
- [x] 6.2 实现创建记录（pending → generating → completed/failed 状态流转）
- [x] 6.3 实现查询历史列表（分页 + 按类型/状态筛选 + 按创建时间倒序）
- [x] 6.4 实现查询详情（权限校验：只能查看自己的记录）
- [x] 6.5 实现删除记录（联动 S3/本地文件清理 + 配额释放）
- [x] 6.6 无存储模式下记录元数据（storage_type='upstream'，不累加配额）

## 7. Sora 客户端 Handler 与路由（sora-generation-gateway）

- [x] 7.1 新增 `handler/sora_client_handler.go`：客户端 API Handler
- [x] 7.2 实现 `POST /api/v1/sora/generate`（异步）：配额检查 → 并发数检查(≤3) → 创建 pending 记录 → **立即返回 generation_id** → 后台异步(Forward → 自动上传S3/降级 → 更新记录 → 累加配额)
- [x] 7.3 实现 `GET /api/v1/sora/generations`：历史列表接口（支持 status/storage_type/media_type 筛选；S3 记录动态生成预签名 URL）
- [x] 7.4 实现 `GET /api/v1/sora/generations/:id`：详情接口（动态预签名 URL）
- [x] 7.5 实现 `DELETE /api/v1/sora/generations/:id`：删除接口
- [x] 7.6 实现 `GET /api/v1/sora/quota`：配额查询接口
- [x] 7.7 实现 `GET /api/v1/sora/models`：可用模型列表接口
- [x] 7.8 注册路由：`server/routes/` 新增 `/api/v1/sora/*` 路由组
- [x] 7.9 调整 `/sora/v1/chat/completions` 直调路径：保持纯透传，不执行本地/S3 媒体落盘
- [x] 7.10 实现 `POST /api/v1/sora/generations/:id/save`：手动保存到 S3（仅 upstream 记录，含 URL 过期检测）
- [x] 7.11 实现 `POST /api/v1/sora/generations/:id/cancel`：取消生成任务（标记 cancelled，忽略后续结果）
- [x] 7.12 实现 `GET /api/v1/sora/storage-status`：返回 { s3_enabled, s3_healthy, local_enabled }

## 8. 管理员配额管理界面

- [x] 8.1 系统设置页面：新增"Sora 默认存储配额"设置项 — 集成在 Sora S3 存储配置卡片中
- [x] 8.2 用户管理页面：用户编辑表单新增"Sora 存储配额"字段
- [x] 8.3 分组管理页面：分组编辑表单新增"Sora 存储配额"字段
- [x] 8.4 后端 API 适配：用户/分组的创建和更新接口支持新增字段

## 9. Sora 客户端前端 - 基础框架（sora-client-ui）

- [x] 9.1 新增 `views/user/SoraView.vue`：Sora 客户端主页面容器（暗色主题）
- [x] 9.2 新增 `components/sora/SoraNavBar.vue`：页面内导航栏（仅 Tab 切换 + 配额条，不含 Logo/头像）— Tab 导航集成在 SoraView.vue 中
- [x] 9.3 前端路由注册：`router/index.ts` 新增 `/sora` 路由（`requiresAuth: true, requiresAdmin: false`）
- [x] 9.4 侧边栏菜单：`AppSidebar.vue` 新增 Sora 菜单项（Sparkles 线性图标，`hideInSimpleMode: true`），同时添加到 `userNavItems`（Dashboard 之后）和 `personalNavItems`（API 密钥之后），条件显示 `sora_client_enabled`
- [x] 9.5 API 模块：新增 `api/sora.ts`，封装所有 Sora 客户端 API 调用
- [x] 9.6 后端公共设置 API：新增 `sora_client_enabled` 字段到公共设置响应（根据活跃 Sora 账号数 > 0 推断）
- [x] 9.7 功能未启用提示页：用户直接访问 `/sora` 但 `sora_client_enabled = false` 时显示提示

## 10. Sora 客户端前端 - 生成页

- [x] 10.1 新增 `components/sora/SoraGeneratePage.vue`：生成页主容器（多任务时间线布局）
- [x] 10.2 新增 `components/sora/SoraPromptBar.vue`：底部创作栏（提示词输入 + 参数选择 + 生成按钮 + 活跃任务计数）
- [x] 10.3 新增 `components/sora/SoraModelSelector.vue`：模型选择下拉（视频/图片分组）— 集成在 SoraPromptBar 中
- [x] 10.4 新增 `components/sora/SoraProgressCard.vue`：生成进度卡片（6 种状态：pending/generating/completed-s3/completed-upstream/failed/cancelled）
  - pending/generating：显示已等待时长 + 预估剩余 + 取消按钮
  - completed-s3：显示"✓ 已保存到云端" + 本地下载
  - completed-upstream：显示 15 分钟倒计时 + 本地下载 + 保存到存储
  - failed：分类错误信息 + 重试/编辑后重试/删除
  - cancelled：已取消 + 重新生成/删除
- [x] 10.5 新增 `components/sora/SoraNoStorageWarning.vue`：无存储提示组件
- [x] 10.6 实现 Ctrl/Cmd + Enter 快捷键触发生成
- [x] 10.7 实现图片模型切换时隐藏方向/时长选择
- [x] 10.8 实现参考图上传功能
- [x] 10.9 实现前端轮询机制：递减频率（3s→10s→30s）轮询 GET /api/v1/sora/generations/:id
- [x] 10.10 实现页面加载时恢复进行中任务（GET /api/v1/sora/generations?status=pending,generating）
- [x] 10.11 实现浏览器通知（Notification API）：任务完成/失败时通知 + 标签页 title 闪烁
- [x] 10.12 实现 beforeunload 警告：存在未下载的 upstream 记录时阻止离开
- [x] 10.13 实现 upstream 记录的 15 分钟倒计时 UI（进度条 + 红色警告态）
- [x] 10.14 实现取消生成功能：调用 POST /api/v1/sora/generations/:id/cancel + 二次确认

## 11. Sora 客户端前端 - 作品库页

- [x] 11.1 新增 `components/sora/SoraLibraryPage.vue`：作品库页主容器（请求 storage_type=s3,local 筛选已保存作品）
- [x] 11.2 新增 `components/sora/SoraLibraryGrid.vue`：响应式网格布局（CSS Grid auto-fill, 4→3→2→1 列）— 集成在 SoraLibraryPage 中
- [x] 11.3 新增 `components/sora/SoraMediaCard.vue`：作品卡片（缩略图 + 类型角标 + hover 显示下载和删除）— 集成在 SoraLibraryPage 中
- [x] 11.4 新增 `components/sora/SoraEmptyState.vue`：空状态引导（图标 + "暂无作品" + "开始创作"按钮）— 集成在 SoraLibraryPage 中
- [x] 11.5 实现全部/视频/图片筛选功能
- [x] 11.6 实现分页加载（滚动加载或按钮加载）

## 12. Sora 客户端前端 - 弹窗与辅助组件

- [x] 12.1 新增 `components/sora/SoraMediaPreview.vue`：作品详情预览弹窗
- [x] 12.2 新增 `components/sora/SoraQuotaBar.vue`：配额进度条组件
- [x] 12.3 新增 `components/sora/SoraDownloadDialog.vue`：即时下载弹窗（无存储模式）
- [x] 12.4 实现视频缩略图：前端用 `<video>` 标签截取第一帧 — 使用 hover 播放方式实现

## 13. 国际化

- [x] 13.1 添加 Sora 客户端中文翻译文本（生成页、作品库、配额、错误提示等）
- [x] 13.2 添加 Sora 客户端英文翻译文本
- [x] 13.3 添加 Sora API Key 账号相关的中英文翻译文本 — 之前已有
- [x] 13.4 添加 Sora S3 存储设置相关的中英文翻译文本

## 14. 集成测试与验证

- [x] 14.1 验证 API Key 直接调用路径 (`/sora/v1/chat/completions`) 保持完全向后兼容
- [x] 14.2 验证客户端 UI 异步生成完整流程：generate → 立即返回 → 轮询 → 自动 S3 上传 → 记录 → 配额
- [x] 14.3 验证三层降级链：S3 → 本地 → 上游 URL
- [x] 14.4 验证配额超限拒绝、引导对话框和释放逻辑
- [x] 14.5 验证 Sora apikey 账号 HTTP 透传和 sub2api 级联部署
- [x] 14.6 验证无存储模式下的倒计时 + 即时下载 + beforeunload 警告
- [x] 14.7 验证数据库迁移脚本的向后兼容性（additive only）
- [x] 14.8 验证 Sora apikey `base_url` 非法输入拦截和 URL 规范化拼接（避免双斜杠）
- [x] 14.9 验证 `/sora/v1/chat/completions` 路径不再创建本地媒体文件
- [x] 14.10 验证取消生成功能：pending/generating 可取消，cancelled 不累加配额
- [x] 14.11 验证手动保存到存储：upstream 记录 → 点击保存 → S3 上传 → 状态更新 → 配额累加
- [x] 14.12 验证页面刷新恢复：刷新后自动恢复所有进行中任务卡片
- [x] 14.13 验证多任务并发：同时 3 个任务正常运行，第 4 个被拒绝
- [x] 14.14 验证预签名 URL 动态刷新：作品库每次打开获取新 URL，不出现碎图
- [x] 14.15 验证浏览器通知：任务完成/失败时桌面通知 + 标签页 title 闪烁
- [x] 14.16 验证 Sora 菜单条件显示：无 Sora 账号时侧边栏不显示 Sora 入口；添加 Sora 账号后自动出现
- [x] 14.17 验证双菜单同步：普通用户和管理员"我的账户"均能看到 Sora 菜单项
- [x] 14.18 验证简单模式：开启 simpleMode 后 Sora 菜单项隐藏
- [x] 14.19 验证 Sora 页面嵌入布局：Sora 页面在全局侧边栏内渲染，侧边栏可正常切换其他页面
