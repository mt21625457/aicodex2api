## ADDED Requirements

### Requirement: Forward-Compatible Performance Rollout

所有高影响前端性能优化 SHALL 支持开关化发布，并默认保持旧行为，以保证向前兼容和无中断升级。

#### Scenario: 默认行为兼容旧版本
- **WHEN** 新版本部署但性能开关未开启
- **THEN** 应用行为 SHALL 与旧版本保持一致
- **AND** 不得因优化代码引入功能语义变化

#### Scenario: 灰度期间可单项回退
- **WHEN** 某项优化在灰度中触发错误率或回归告警
- **THEN** 系统 SHALL 支持仅回退该项开关
- **AND** 其他已稳定优化项保持生效

### Requirement: Runtime Flag Source and Backward Compatibility

性能开关 SHALL 优先来自运行时公共配置，并保证旧客户端对新增字段的向前兼容。

#### Scenario: 运行时配置优先于构建默认值
- **WHEN** `public_settings.perf_flags` 与本地构建默认值同时存在
- **THEN** 前端 SHALL 优先采用 `public_settings.perf_flags`
- **AND** 本地构建默认值仅作为缺省兜底

#### Scenario: 旧前端忽略新增配置字段
- **WHEN** 服务端返回旧前端未知的 `perf_flags` 字段
- **THEN** 旧前端 SHALL 忽略未知字段并保持现有行为
- **AND** 不得出现初始化失败或页面不可用

### Requirement: Metrics-Gated Progressive Enablement

优化能力启用 SHALL 受指标门禁控制，不满足阈值不得推进下一阶段。

#### Scenario: 指标不达标阻断全量
- **WHEN** 灰度期间关键指标（错误率、关键页面加载）不达标
- **THEN** 发布流程 SHALL 阻断全量
- **AND** 自动进入回退或继续观察流程

#### Scenario: 指标达标后分阶段推进
- **WHEN** 灰度指标连续达标
- **THEN** 允许按 10% -> 50% -> 100% 分阶段推进
- **AND** 每阶段均需保留回退能力

#### Scenario: 不满足门禁时禁止推进
- **WHEN** 任一阶段未满足连续 24 小时门禁阈值
- **THEN** 发布流程 SHALL 禁止进入下一阶段
- **AND** SHALL 触发回退或继续观察
