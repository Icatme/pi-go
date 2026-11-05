# M8b：宿主授权的原操作状态刷新

2026-11-05。基于未合并 draft #182 HEAD
`f13027d07132cbc88eca781e1297818c5ffdc051`，精确树
`d1a1089369e1824f252265d3317611956a83d190`。开发切片，不是生产验收。

## 范围与入口

M8a 的 Check/RunWatchdog 仍只读，模型诊断仍是假说。新增独立的宿主入口
`RefreshWatchdog(ctx, request, authorize)`，只对已经准入的一个后台操作调用
**原 Backend 的 Inspect(原 ID)**。没有通用动作字符串、模型工具、Start、Stop、
重连调度、取消、Steer、任务重试、改派、发送或 Reconcile 入口。原生权限和凭证
继续由原宿主/backend 持有；这个入口不授予新的原生权限。

请求固定绑定 FindingID、TaskID、完整观察 Revision、OperationID、原始 Origin
运行身份及 DeliveryID。运行中的当前任务目标另从 finding/current task 比较。
必须匹配最新非 stale 诊断及当前事实；诊断前后的新输入、报告、运行、审核或
操作状态变化都会让旧决定失效。已明确绑定任务、但尚未完成路由的新输入同样
失效。Revision 包含 RuntimeID，重启前未消费的诊断不能授权重启后的新请求。

独立且非 nil 的宿主 authorizer 必须明确批准这个绑定；它不是 Diagnose 的
输出，也不是模型可选择的回调。回调只收到有界身份，不收到工具参数、结果、
receipt、诊断文本或凭证。回调需要只读、响应取消，不得从回调中 Close 协调器。
拒绝或 panic 不记录其原始错误，也不调用 Inspect。已拒绝的申请不占用诊断。

## 持久语义

1. 首先验证绑定，再在锁外请求宿主授权；授权返回后再次核对当前事实。
2. 以 FindingID + OperationID 为唯一键，先提交 `pending` 干预记录，然后才
   调用 Inspect。ID 由这两个原始身份确定，调用方不能换任意 request ID 绕过。
3. Inspect 返回后再次检查上下文、租约与事实。仍有效时，后台观察与干预结果
   在**同一条日志**提交，包括真实退出带来的配额释放和原任务的唯一 completion。
   事实已变化则只记录 `stale`，不应用过期结果。
4. 完成记录保存 `unknown/failed/running/exited/stale`、公开操作状态、记录时间、
   下一复查时间和 `new_finding_and_host_authorization` 条件。它不是任务成功证明。
   未完成记录的条件是 `inspect_outcome_unrecorded`：Inspect 可能执行，也可能没
   执行，不能据此推断安全重发或完成。

已有记录的完全相同请求只返回原记录，不再次授权或 Inspect。并发调用在任一方
持久预留前，可能分别执行只读 authorizer，但只有一方能够持久预留并 Inspect。
重启不会恢复未完成干预。后续观察需要新一轮 finding 和新宿主决定；相同 finding
不能重复用于同一操作。已有 M4 后台 watcher 仍按它原来的恢复契约运行，不受
本次干预去重约束；若存在既有持久 Stop 意图，原 watcher 仍可能恢复该 Stop。
这不是本 API 新增的停止或自动修复能力。

`RecheckAt` 是记录时的宿主墙钟加固定 policy interval，仅供复查提示，不是执行
定时器、授权到期计时器或自动排队请求。M8a 可配置的诊断时钟仍不控制租约。
新 finding、状态变化和进程重启使旧绑定失效；仅时间经过不证明任务卡住。

## 证据、隐私与存储边界

- 复用已有后台观察验证及单调 receipt 合并。unknown、错误、panic、错误 ID、
  非法/倒退证据都保留审核与配额；只有确切 exited 证明才释放配额。退出失败或
  已请求停止仍保持审核，本 API 不做宿主复核。running 证据沿用已有恢复语义。
- 合法退出可能给**既有 Worker**送一个私有 completion 输入，不创建新任务或
  新操作。该 Worker 保留其既有授权，并非这个刷新调用新增的外部执行许可。
- 公共 `WatchdogStatus.Interventions` 和返回值不含参数、输出、receipt、私有
  backend/授权错误或模型快照。后台私有输出仍只存入已有私有操作记录。
- 所有预留和结果走原协调器 writer/租约 fence。失租后提交失败；关闭/失租会
  取消回调，Close 等它退出。读操作与租约变化不是跨主机原子事务，native backend
  仍需自己的原始 epoch/认证边界；刷新本身不调用有副作用的 backend 方法。
- 重放严格验证未混合 envelope、预留先于结果、不可变绑定、结果和复查条件，
  以及当前可重放任务事实。运行身份从持久 running delivery 重建，不把进程内
  counters 或 RuntimeID 当作可重放证据。新输入之后伪造的旧授权结果被拒绝。
- pending 提交失败时不 Inspect；结果提交失败时不应用终态/配额/完成输入。
  记录会保持 pending，协调器 fail-stop，不把部分结果宣称为成功。

## 验收与边界

先建立缺 API 的编译失败测试，再实施。离线覆盖：授权缺失/拒绝/panic、身份
交叉替换、最新诊断/新输入/新进程失效、授权阻塞期间事实变化、并发重复、
Inspect 阻塞期间新诊断/后台终态、关闭等待、pending 重开不恢复、两阶段写失败、
unknown/部分 committed receipt/错误/失败退出、公共隐私 canary、混合/重复/伪造
日志、已有 Worker 运行中的合法重放、终态原操作唯一 completion 及外部 Go 示例。

真实 sharedsqlite Close/reopen 新 epoch 接管覆盖授权阶段和 Inspect 阶段的旧 owner
拒绝，直接旧 journal append 也被 fence。所有 backend 都是测试替身，不触达真实
设备、模型或平台；没有真实外部副作用。独立只读审核指出重放时新输入可能未使
旧绑定失效，已补充可重放事实比较与直接 replay 反例后复审。

最终全仓 tests/race/vet、examples、focused race 和精确树证据见 draft PR。
本远程 Linux 容器没有 PowerShell，使用 POSIX shell、官方 Go 1.26.10、缓存模块、
`GOPROXY=off` 与 `-mod=readonly`。不改依赖，不手动触发 CI。

这仍不是完整 M8：通用修复动作、跨进程控制平面、策略迁移、人工升级通知、
自动复查/重连/任务恢复、真实效果核实及日志压缩均未实现。本次也没有生产平台、
真实设备/账户、用户本机、Codex/额外付费执行、合并或部署验收。
