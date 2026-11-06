# M8c：干预后的只读复查证据

2026-11-06，基于草稿 #194 的 `2dc72751`。本切片补上 M8b 的到期复查提示到
持久观察记录之间的缺口，不实现自动修复或消息发送。

## 行为与入口

`RefreshWatchdog` 的 completed 记录已有 `RecheckAt`，但普通巡检只按任务游标
轮转。现在，在宿主已有的 `CheckWatchdog` / `RunWatchdog` 合格巡检节拍中，
额外选择已经到期、尚未记录后续观察的 completed 干预。选择按原预留顺序进行，
每轮最多 `Policy.BatchSize` 项，独立于普通任务游标，不扩大原模型诊断任务批次。

复查只读取协调器已经提交的事实：原后台操作当前公开状态、审核屏障，以及
任务的持久状态、待处理数量、已关联输入引用和最近公开报告。普通 Probe/Diagnose
回调结束后，才在最终提交锁内捕获这些事实，避免把回调开始前的任务状态当成
当前状态。原巡检的 stale finding 规则仍保留，两种证据不混为一个判断。

复查不会再次调用 Inspect、Probe 或 Diagnose，不启动另一计时器，不重放宿主
授权，不调用 Start/Stop/Steer/Reconcile，不创建任务、completion、Router 输入或
回复。普通巡检原本配置的只读回调仍照常执行；原后台恢复 watcher 仍遵守自己的
既有协议，不是本次复查新增的行为。

pending 干预不参与，既不视为完成，也不自动恢复其 Inspect。每个 completed
干预最多产生一条后续观察，后来新完成或后来才到期的记录仍会被后续轮次发现。
需要再次刷新后端时，仍须新的 finding 和宿主授权。

## 证据、身份与时间

公开 `WatchdogFollowup` 包含稳定顺序/ID、InterventionID、原始 refresh Request、
wall-clock ObservedAt、`Source=coordinator_observation`、公开 Operation 和精简
Task。Request 永远指向原任务、操作、delivery 与运行身份。Task 可以反映该任务
后来的输入或运行；这不会把旧干预的授权转移到新运行。

分类按以下顺序保守生成：

- 原操作或当前任务仍需审核：`needs_review`。
- 已提交原操作退出：`operation_exited`。
- 已提交原操作运行中：`operation_running`。
- 其余已准入、仍预留的原操作：`operation_reserved`。

退出不是任务成功或修复有效证明。Task.LastStatus 是上次持久结果/复核状态，
不是当前运行已完成的保证；RunningOrigin/RunningDeliveryID 独立说明当前日志里
尚未结算的运行。最近输入仅指已经关联到此任务的输入，报告仍是既有公开摘要。
公开记录不含参数、后台输出、receipt、私有错误、Worker/模型快照、RuntimeID
或进程内工具/turn 计数。Observation 不重新向后端查证，可能落后于现实。

M8b 的 RecheckAt 来自宿主 wall clock，因此复查到期判断和 ObservedAt 继续用
wall clock，与可替换的 WatchdogClock 分离。后者只决定普通巡检何时合格；租约
仍有自己的时钟。时钟回拨、巡检暂停或回调延迟会推迟复查，不为此引入额外执行。
重放使用记录中的时间核对到期关系，不读取当前时间重新生成历史事实。

## 宿主分页消费

`WatchdogFollowupsSince(after uint64, limit int)` 返回 `WatchdogFollowupPage`：

- 从排他游标之后读取独立副本；零游标从第一条开始。
- limit 为 1–32；非正数和超过 32 的值按 32 处理。
- Cursor 是本页最后一项序号，无新项时保留传入游标；HasMore 表示还有已提交项。
- 超过当前日志的游标不会倒退。宿主须把游标和同一个持久 journal 域绑定，不能
  将另一个日志的游标用于这里，也不要凭空跳到未来序号。

宿主在自己可靠处理此批后保存 Cursor。需要对外通知时，走宿主另行授权的
投递与幂等机制；此接口不是发送队列或送达回执，不承诺外部 exactly-once。
重新从零读取可供审计，但消费者应按同日志域下的稳定 ID 去重。
可执行公开 Go 示例见 `ExampleSuperAgent_WatchdogFollowupsSince`。

## 持久化与升级边界

本轮普通检查、其下一游标/模型快照和到期复查批次在同一条
`watchdog_checked_followups` 记录中提交，沿用唯一 writer 和所有权 fence。
已完成记录不会重写 Intervention 或其 RecheckCondition。重开恢复已提交的记录，
未记录的到期项只在宿主恢复合格巡检后处理，不因 Open 本身而额外复查或发消息。

重放重建完整应到前缀并比较身份、顺序、时钟、分类及全部公开事实。遗漏、重复、
重绑、提前时间、伪造 task/report、混合业务/操作 envelope 和非法游标均拒绝。
写失败不推进检查或复查索引；写已成功但回执丢失时 live 协调器 fail-stop，重开
恢复同一原子记录，不会重复追加同一复查记录。失租后的迟到普通诊断不能提交任何复查。

**旧日志可升级，但新巡检记录不能由旧版本降级打开。** 新代码仍读取原
`watchdog_checked` 记录；首次写新状态后禁止退回旧 check 状态。新巡检即使没有
到期项，也写新状态并保存复查 wall clock。旧实现因不认识新状态而拒绝打开，
避免静默丢掉后续观察。没有改变 session JSONL 格式版本、数据库 schema 或
后端接口，但宿主升级仍需把这一协调器记录兼容边界纳入版本安排。

## 验证与限制

定向覆盖到期边界、pending 排除、前缀/批量、五任务普通游标独立、回调期间
新增任务输入、原操作退出、审核/配额不变、公开 canary、取消/写失败、重开去重、
完整 JSONL 语义伪造、旧记录升级、未知提交恢复，以及真实临时 SQLite 新 epoch
接管。伪造夹具保留合法外层 session 链，避免因错误的链结构而误判验证有效。

最终代码在远程 Linux/amd64、Go 1.26.10 上通过根库 readonly tests/vet、
`superagent/...`、`agent/session` 与 `cmd/pi-superagent` 的 race、examples 独立
模块 tests/vet、定向 race 重复、gofmt 和 diff 检查。新增分页与运行中重放测试后，
再次通过根库 tests/vet 和整个 SuperAgent 包 race。独立源码、测试与文档审查无
阻塞项。没有完整根库 race 或 Windows/macOS 执行；临时 SQLite 接管不代表真实
数据库服务、跨主机或生产验收。最终使用缓存依赖和 `GOPROXY=off`，未改模块版本。
本容器无 PowerShell，使用 POSIX shell。

每轮输出条数有界，不代表扫描开销或全部历史存储有界；选择仍读取已保留的
干预/复查和任务历史。没有连续监控每次干预、自动升级通知、通用修复、策略
版本迁移或真实后端效果核实。没有新模型调用、生产/真实数据库服务、用户电脑、
额外付费执行、CI 调度、发布、合并或部署。
