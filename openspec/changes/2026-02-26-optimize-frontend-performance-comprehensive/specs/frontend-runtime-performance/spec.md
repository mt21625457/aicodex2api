## ADDED Requirements

### Requirement: Runtime Work Avoidance

前端运行时 SHALL 避免可识别的重复调用与不必要计算，优先降低高频路径上的 CPU 与内存开销。

#### Scenario: 鉴权初始化仅执行一次
- **WHEN** 应用首次完成路由初始化
- **THEN** 鉴权状态恢复流程 SHALL 只执行一次
- **AND** 不应由多个页面重复触发相同初始化逻辑

#### Scenario: 管理设置拉取单入口触发
- **WHEN** 侧边栏初始化管理设置
- **THEN** 仅允许一个入口触发 `adminSettingsStore.fetch`
- **AND** 不得出现 watch 与 mounted 双入口重复触发

#### Scenario: 大数据表避免客户端重排序
- **WHEN** 表格数据量超过运行时阈值（例如 200 行）
- **THEN** 前端 SHALL 使用服务端排序或等效轻量策略
- **AND** 不得在每次渲染周期执行全量客户端排序

### Requirement: Lazy Initialization for Non-Critical Features

非关键能力（引导、公告详情渲染）SHALL 采用惰性初始化，避免进入公共链路初始执行。

#### Scenario: 引导能力按需加载
- **WHEN** 用户未触发引导功能
- **THEN** `driver.js` 相关代码不得在公共布局初始化阶段执行

#### Scenario: 公告详情按需渲染
- **WHEN** 用户未打开公告面板
- **THEN** 公告详情渲染链路不应触发全量加载与渲染

### Requirement: Deterministic and Side-Effect-Free Rendering

模板层渲染 SHALL 避免副作用表达式，确保渲染行为稳定可预测。

#### Scenario: 渲染列表不变异源数组
- **WHEN** 组件展示已选错误码列表
- **THEN** 组件 SHALL 使用不可变排序结果进行渲染
- **AND** 不得在模板表达式中直接调用会变异原数组的 `sort()`
