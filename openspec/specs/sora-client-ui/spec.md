## ADDED Requirements

### Requirement: Sora 客户端路由与菜单
系统 SHALL 在前端新增 Sora 客户端页面，可通过侧边栏菜单访问。菜单项的显示须与现有侧边栏风格一致，并遵循条件显示、简单模式、双菜单同步等现有模式。

#### Scenario: 路由注册
- **WHEN** 前端路由初始化
- **THEN** 系统 SHALL 注册 `/sora` 路由，加载 `SoraView.vue` 页面
- **AND** 路由 meta SHALL 设置 `requiresAuth: true, requiresAdmin: false`

#### Scenario: 侧边栏菜单项（条件显示）
- **WHEN** 用户登录后查看侧边栏
- **AND** 公共设置 `sora_client_enabled` 为 true（后端根据是否存在活跃 Sora 账号自动推断）
- **THEN** 侧边栏 SHALL 显示"Sora"菜单项
- **AND** 菜单项 SHALL 使用 Heroicons 线性风格的 Sparkles 图标（与现有侧边栏图标统一为 stroke 风格，`h-5 w-5`）
- **AND** 点击后 SHALL 跳转到 `/sora` 页面

#### Scenario: 菜单项在管理员未启用 Sora 时隐藏
- **WHEN** 公共设置 `sora_client_enabled` 为 false（无活跃 Sora 账号）
- **THEN** 侧边栏 SHALL 不显示"Sora"菜单项
- **AND** 用户直接访问 `/sora` 时 SHALL 显示功能未启用提示页

#### Scenario: 菜单位置与双菜单同步
- **WHEN** Sora 菜单项显示
- **THEN** 对于普通用户（`userNavItems`），Sora SHALL 位于"Dashboard"之后、"API 密钥"之前
- **AND** 对于管理员"我的账户"区域（`personalNavItems`），Sora SHALL 位于"API 密钥"之后、"使用记录"之前
- **AND** 两个菜单列表 SHALL 同步添加（确保管理员和普通用户均可访问）

#### Scenario: 简单模式隐藏
- **WHEN** 系统处于简单模式（`isSimpleMode = true`）
- **THEN** Sora 菜单项 SHALL 隐藏（`hideInSimpleMode: true`）

### Requirement: Sora 客户端页面内导航
系统 SHALL 在 Sora 客户端页面顶部显示页面内导航栏，仅包含 Tab 切换和配额信息。Sora 页面嵌入在全局侧边栏布局内，不独立展示 Logo 或用户头像（这些已由全局侧边栏提供）。

#### Scenario: 页面内导航栏显示
- **WHEN** 用户进入 Sora 客户端页面
- **THEN** 页面顶部 SHALL 显示页面内导航栏，包含"生成"/"作品库" Tab 切换
- **AND** 右侧 SHALL 显示配额进度条（如 "2.1GB / 5GB"）
- **AND** 导航栏 SHALL 不包含 Logo 和用户头像（避免与全局侧边栏重复）
- **AND** Sora 页面 SHALL 保留在全局侧边栏布局内渲染（用户可通过侧边栏随时切换到其他页面）

#### Scenario: Tab 切换
- **WHEN** 用户点击"生成"或"作品库" Tab
- **THEN** 页面 SHALL 切换到对应视图，不刷新页面

### Requirement: 生成页面 - 底部创作栏
系统 SHALL 在生成页底部固定显示创作栏，用于输入提示词和配置生成参数。

#### Scenario: 提示词输入
- **WHEN** 用户在创作栏输入提示词
- **THEN** 输入框 SHALL 支持多行文本，自动扩展高度
- **AND** 支持 Ctrl/Cmd + Enter 快捷键触发生成

#### Scenario: 模型选择
- **WHEN** 用户点击模型选择器
- **THEN** 系统 SHALL 从 `GET /api/v1/sora/models` 获取可用模型列表
- **AND** 下拉菜单 SHALL 按视频模型和图片模型分组显示

#### Scenario: 视频参数配置
- **WHEN** 用户选择视频模型
- **THEN** 创作栏 SHALL 显示方向选择（横屏/竖屏/方形）和时长选择（10s/15s/25s）

#### Scenario: 图片模型隐藏视频参数
- **WHEN** 用户选择图片模型（如 gpt-image-1）
- **THEN** 创作栏 SHALL 隐藏方向选择和时长选择

#### Scenario: 参考图上传
- **WHEN** 用户点击图片上传按钮
- **THEN** 系统 SHALL 允许上传参考图片，作为生成输入的 `image_url`

### Requirement: 生成页面 - 发起生成
系统 SHALL 通过底部创作栏的"生成"按钮发起 Sora 生成请求。

#### Scenario: 发起视频生成
- **WHEN** 用户填写提示词并点击"生成"按钮
- **AND** 当前选择视频模型
- **THEN** 系统 SHALL 发送 `POST /api/v1/sora/generate`，包含 `prompt`、`model`、`media_type=video`、方向、时长参数
- **AND** 页面 SHALL 显示新的进度卡片（pending 状态）

#### Scenario: 发起图片生成
- **WHEN** 用户填写提示词并选择图片模型后点击"生成"
- **THEN** 系统 SHALL 发送生成请求，`media_type=image`
- **AND** 页面 SHALL 显示新的进度卡片

#### Scenario: 配额不足预防与提示
- **WHEN** 用户配额使用率超过 90%
- **THEN** 配额进度条 SHALL 变为黄色警告色，提示"存储空间即将用完"
- **AND** 配额使用率达 100% 时，"生成"按钮 SHALL 禁用并显示 tooltip "存储配额已满"

#### Scenario: 配额不足错误引导
- **WHEN** 生成请求返回 HTTP 429（配额不足）
- **THEN** 页面 SHALL 弹出配额不足对话框，包含：
  - 当前配额使用详情（已用 / 总配额）
  - 引导文案"您可以在作品库中删除不需要的作品来释放存储空间"
  - "前往作品库"按钮（直接跳转到作品库页面）

### Requirement: 生成页面 - 进度展示
系统 SHALL 在生成页中间区域实时展示当前生成任务的进度状态。

#### Scenario: 排队中状态
- **WHEN** 生成记录 `status = 'pending'`
- **THEN** 进度卡片 SHALL 显示"排队中"状态、灰色状态指示、提示词摘要（前 50 字）
- **AND** SHALL 显示"取消"按钮

#### Scenario: 生成中状态
- **WHEN** 生成记录 `status = 'generating'`
- **THEN** 进度卡片 SHALL 显示"生成中"动画、提示词预览
- **AND** SHALL 显示已等待时长（如"已等待 3:42"）和预估剩余时间（如"预计剩余 8 分钟"）
- **AND** SHALL 显示"取消"按钮
- **AND** 超过 20 分钟未完成时 SHALL 显示"生成时间异常，建议取消重试"

#### Scenario: 生成完成 - 自动保存成功
- **WHEN** 生成记录 `status = 'completed'` 且 `storage_type = 's3'`
- **THEN** 进度卡片 SHALL 显示生成结果预览（视频播放器或图片缩略图）
- **AND** SHALL 显示 "✓ 已保存到云端" 状态标识
- **AND** SHALL 提供"📥 本地下载"按钮
- **AND** 作品自动出现在作品库中

#### Scenario: 生成完成 - 降级本地存储
- **WHEN** 生成记录 `status = 'completed'` 且 `storage_type = 'local'`
- **THEN** 进度卡片 SHALL 显示 "✓ 已保存到本地" 状态标识
- **AND** SHALL 提供"📥 本地下载"按钮

#### Scenario: 生成完成 - 无存储（upstream）
- **WHEN** 生成记录 `status = 'completed'` 且 `storage_type = 'upstream'`
- **THEN** 进度卡片 SHALL 显示"📥 本地下载"按钮
- **AND** SHALL 显示 15 分钟过期倒计时进度条（基于 `completed_at` 计算）
- **AND** 若 S3 当前可用，SHALL 显示可点击的"☁️ 保存到存储"按钮
- **AND** 若 S3 不可用，"☁️ 保存到存储"按钮 SHALL 禁用并 tooltip "管理员未开通云存储"
- **AND** 倒计时结束后 SHALL 禁用所有按钮并显示"链接已过期"

#### Scenario: 生成失败状态
- **WHEN** 生成记录 `status = 'failed'`
- **THEN** 进度卡片 SHALL 显示分类错误信息：
  - 上游服务错误 → "服务暂时不可用，建议稍后重试"
  - 内容审核不通过 → "提示词包含不支持的内容，请修改后重试"
  - 超时 → "生成超时，建议降低分辨率或时长后重试"
- **AND** SHALL 提供"重试"按钮（一键以相同参数重新发起）
- **AND** SHALL 提供"编辑后重试"按钮（将参数回填到创作栏）
- **AND** SHALL 提供"删除"按钮

#### Scenario: 任务取消状态
- **WHEN** 生成记录 `status = 'cancelled'`
- **THEN** 进度卡片 SHALL 显示"已取消"灰色状态
- **AND** SHALL 提供"重新生成"和"删除"按钮

### Requirement: 生成页面 - 多任务管理与状态恢复
系统 SHALL 支持多个并发生成任务的展示和页面刷新后的状态恢复。

#### Scenario: 多任务并发展示
- **WHEN** 用户有多个进行中或刚完成的生成任务
- **THEN** 生成页中间区域 SHALL 以时间线方式纵向排列所有任务卡片，最新在最上方
- **AND** 底部创作栏 SHALL 显示当前活跃任务数（如"正在生成 2/3"）
- **AND** 超过并发上限（3 个）时，"生成"按钮 SHALL 禁用并提示"请等待当前任务完成"

#### Scenario: 页面刷新后恢复任务
- **WHEN** 用户刷新页面或重新进入 Sora 客户端
- **THEN** 系统 SHALL 调用 `GET /api/v1/sora/generations?status=pending,generating` 获取进行中任务
- **AND** SHALL 自动恢复所有进度卡片的显示
- **AND** SHALL 继续对进行中任务执行轮询

#### Scenario: 前端轮询策略
- **WHEN** 存在 pending 或 generating 状态的任务
- **THEN** 前端 SHALL 按递减频率轮询 `GET /api/v1/sora/generations/:id`：
  - 0-2 分钟：每 3 秒
  - 2-10 分钟：每 10 秒
  - 10-30 分钟：每 30 秒
- **AND** 每次轮询结果 SHALL 更新卡片显示
- **AND** 卡片上 SHALL 显示"最后更新：N 秒前"以确认数据实时性

#### Scenario: 浏览器通知
- **WHEN** 生成任务完成或失败
- **AND** 浏览器标签页不在前台
- **THEN** 系统 SHALL 通过 Notification API 发送桌面通知
- **AND** 标签页 title SHALL 闪烁提示（如"(1) ✓ 生成完成 - Sora"）

### Requirement: 生成页面 - 无存储提醒
系统 SHALL 在未配置存储时显示醒目提示。

#### Scenario: 无存储警告
- **WHEN** 用户进入生成页
- **AND** S3 和本地存储均未配置
- **THEN** 创作栏 SHALL 显示警告标签"存储未配置，生成后请立即下载"

#### Scenario: S3 可用时自动保存（正常模式）
- **WHEN** 管理员已开通 S3 存储
- **AND** 用户存储配额未超限
- **THEN** 生成完成后系统 SHALL 自动上传到 S3
- **AND** 卡片 SHALL 显示"✓ 已保存到云端"

#### Scenario: S3 不可用时的降级提示
- **WHEN** 管理员未开通 S3 存储（`sora_s3_enabled = false`）
- **THEN** 生成完成后卡片 SHALL 仅显示"📥 本地下载"按钮
- **AND** "☁️ 保存到存储"按钮 SHALL 禁用并 tooltip "管理员未开通云存储"

#### Scenario: 手动保存到存储（仅 upstream 记录）
- **WHEN** 生成记录 `storage_type = 'upstream'` 且 S3 当前可用
- **THEN** "☁️ 保存到存储"按钮 SHALL 可点击
- **AND** 点击后 SHALL 调用 `POST /api/v1/sora/generations/:id/save`
- **AND** 上传过程中按钮 SHALL 显示 loading 状态
- **AND** 上传成功后按钮 SHALL 变为"✓ 已保存"
- **AND** 上传失败 SHALL 显示错误信息并允许重试

#### Scenario: 无存储生成完成自动提示下载
- **WHEN** 生成完成且 `storage_type = 'upstream'`
- **THEN** 客户端 SHALL 弹出提醒弹窗"文件仅临时保存，请在 15 分钟内下载"
- **AND** SHALL 显示 15 分钟倒计时

#### Scenario: 离开页面未下载警告
- **WHEN** 存在 `storage_type = 'upstream'` 且未过期的完成记录
- **AND** 用户尝试离开或关闭页面
- **THEN** 系统 SHALL 触发 `beforeunload` 事件警告"您有未下载的生成结果，离开后可能丢失"

### Requirement: 作品库页面 - 网格展示
系统 SHALL 在作品库页面以网格布局展示用户的历史生成作品。

#### Scenario: 作品网格显示
- **WHEN** 用户切换到"作品库" Tab
- **THEN** 系统 SHALL 从 `GET /api/v1/sora/generations?storage_type=s3,local` 获取已保存记录
- **AND** SHALL 以响应式网格展示作品卡片（桌面 4 列、平板 3 列、移动端 1-2 列）
- **AND** `storage_type = 'upstream'` 或 `'none'` 的记录 SHALL 不在作品库中显示
- **AND** S3 作品的 URL SHALL 由后端每次请求时动态生成（避免预签名过期）

#### Scenario: 作品卡片信息
- **WHEN** 作品卡片渲染
- **THEN** 每张卡片 SHALL 显示：缩略图/视频预览、类型角标（VIDEO/IMAGE）、模型名称、生成时间
- **AND** 视频卡片 SHALL 显示播放按钮和时长标签

#### Scenario: 卡片 hover 操作
- **WHEN** 用户 hover 作品卡片
- **THEN** SHALL 显示"📥 本地下载"和"🗑 删除"操作按钮
- **AND** 缩略图 SHALL 轻微放大效果（scale 1.05，transition 0.2s）

### Requirement: 作品库页面 - 筛选
系统 SHALL 支持按类型筛选作品。

#### Scenario: 全部/视频/图片筛选
- **WHEN** 用户点击筛选按钮（全部/视频/图片）
- **THEN** 作品网格 SHALL 只显示对应类型的记录
- **AND** SHALL 更新显示作品数量

#### Scenario: 空状态
- **WHEN** 筛选结果为空或用户无任何生成记录
- **THEN** 页面 SHALL 显示空状态引导（图标 + "暂无作品" + "开始创作"按钮）

### Requirement: 作品详情与操作
系统 SHALL 支持查看作品详情和执行下载、删除操作。

#### Scenario: 查看作品详情
- **WHEN** 用户点击作品卡片
- **THEN** 系统 SHALL 弹出预览弹窗，显示完整的媒体内容、提示词、模型信息、生成时间

#### Scenario: 本地下载作品
- **WHEN** 用户点击"本地下载"按钮
- **THEN** 系统 SHALL 触发浏览器下载对应媒体文件

#### Scenario: 保存作品到存储
- **WHEN** 用户点击"保存到存储"按钮
- **AND** 管理员已开通 S3 存储
- **THEN** 系统 SHALL 将媒体文件上传到 S3
- **AND** 更新生成记录的 `storage_type`、`s3_object_keys`
- **AND** 累加用户存储配额

#### Scenario: 删除作品
- **WHEN** 用户点击删除按钮
- **THEN** 系统 SHALL 弹出确认对话框
- **AND** 确认后调用 `DELETE /api/v1/sora/generations/:id`
- **AND** 删除成功后 SHALL 从网格中移除卡片并更新配额显示

### Requirement: 暗色主题设计
系统 SHALL 采用参考 Sora 官方客户端的暗色主题设计。

#### Scenario: 暗色主题样式
- **WHEN** 用户访问 Sora 客户端页面
- **THEN** 页面背景 SHALL 为深黑色（`#0D0D0D`）
- **AND** 文字 SHALL 为白色/浅灰色
- **AND** 卡片和输入框 SHALL 使用多层次灰色（`#1A1A1A`、`#242424`、`#2A2A2A`）
- **AND** 导航栏 SHALL 有毛玻璃效果（`backdrop-filter: blur`）

### Requirement: 响应式布局
系统 SHALL 支持不同屏幕尺寸下的自适应布局。

#### Scenario: 桌面端布局
- **WHEN** 屏幕宽度 > 1200px
- **THEN** 作品网格 SHALL 显示 4 列

#### Scenario: 平板端布局
- **WHEN** 屏幕宽度 900px - 1200px
- **THEN** 作品网格 SHALL 调整为 3 列

#### Scenario: 移动端布局
- **WHEN** 屏幕宽度 < 600px
- **THEN** 作品网格 SHALL 调整为 1-2 列

### Requirement: 国际化支持
系统 SHALL 为 Sora 客户端所有文案提供中英文国际化支持。

#### Scenario: 中文环境
- **WHEN** 系统语言设置为中文
- **THEN** 所有 Sora 客户端文案 SHALL 显示中文

#### Scenario: 英文环境
- **WHEN** 系统语言设置为英文
- **THEN** 所有 Sora 客户端文案 SHALL 显示英文
