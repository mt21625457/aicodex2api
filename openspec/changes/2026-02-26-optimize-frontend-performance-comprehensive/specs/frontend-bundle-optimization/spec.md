## ADDED Requirements

### Requirement: Frontend Bundle Boundary Optimization

Frontend 打包策略 SHALL 按“关键路径、后台重功能、低频工具”进行边界拆分，避免低频重依赖进入高频公共链路。

#### Scenario: 首屏不加载低频重工具
- **WHEN** 用户首次进入首页或常规仪表盘
- **THEN** 构建产物不得在首屏关键链路中加载低频重工具（如 `xlsx`）
- **AND** 与当前首屏路由无关的非关键 vendor 包不得通过首屏 `modulepreload` 强制进入

#### Scenario: 后台重页面按需加载重能力
- **WHEN** 用户未打开账户管理相关重弹窗/重图表
- **THEN** 对应组件代码不得提前随页面主 chunk 一并加载
- **AND** 仅在用户触发后再按需加载

### Requirement: Model Whitelist Payload Decoupling

模型白名单与预设映射 SHALL 支持分片或远程加载，并保留本地快照回退，避免单文件超大硬编码持续膨胀。

#### Scenario: 远程加载失败自动回退
- **WHEN** 远程模型配置请求失败或超时
- **THEN** 系统 SHALL 自动回退到本地快照
- **AND** 用户可继续完成账户配置流程

#### Scenario: 平台级按需加载
- **WHEN** 用户只操作单一平台模型配置
- **THEN** 系统 SHALL 仅加载对应平台的模型数据
- **AND** 不应加载全量平台模型清单
