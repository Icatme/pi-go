# M4c：耐久本地 helper 进程切片

2026-11-05，基于 draft #179 HEAD
`ec9bbe6bcf0d510f0809eeafee24d214132c5fc1`，tree
`c60213e4250d0ef24d4ac9039ef8156a05dceee3`。未合并开发切片。

M4a 的 `background.Local` 注册表仅在进程内存中。新增可选
`agent/background/helper`，由宿主显式注册固定二进制与原生工具；每项操作由独立
helper 进程执行。已有 Local、协调器、设备协议及 JSONL 日志格式不改。
详见 [helper 协议与宿主责任](../agent/background/helper/README.md)。

先添加缺失 API 的失败合同测试，再实现持久不可变请求、操作 ID、原始授权 epoch、
停止意图、回执快照与查询。写入容量预留之后才 dispatch；helper 先持久排他 claim
才运行原生工具。恢复只查原操作，不重启它。PID、进程 Wait、退出码和取消都不能
证明无副作用或允许重试；缺失/损坏/回退证据保持未知和配额。

宿主当前权限回调与耐久 epoch fencing 分开，helper 仍须以原操作授权做原生副作用
前校验。固定命令、参数、工具集、策略、容量和二进制内容绑定在日志头；不提供
shell、凭据、账号、网络监听、沙箱或工具发现。默认不继承环境/标准流/额外句柄。
首次 Start 有最多 5 秒的原生准入回执握手，避免正常进程启动期间错误触发任务审核；
超时仍是结果未知，不撤销原操作或重试。Close 不终止独立 helper。

独立审查推动了以下修复及回归测试：

- 崩溃发生在停止意图落盘和通知之间时，查询恢复重投已持久停止，另行检查当前停止权限。
- 损坏停止文件按取消处理；先读取终态证据，通知失败不阻塞真实终态。
- 旧 running、缺失/损坏快照及存活 PID 不充当重启后的实际进度证据。
- 未准入 tombstone 不占后端配额；回放拒绝不可能的准入、epoch、配额与 tombstone 转移。
- 已返回回执先存入主注册表；16 MiB 读取上限覆盖最坏 JSON 转义，不静默丢回执。
- 实际协调器集成验证正常启动后仍能处理下一输入，重启后未知审核、停止、迟到提交、
  真实退出及唯一完成事件保持原任务/投递/调用关联。

## 验证与限制

真实子进程仅为自行编写的无外部副作用测试 helper。覆盖协调器进程崩溃、helper
崩溃、同 ID 不重启、直接重复 helper claim、停止与迟到提交、原 epoch 撤销。
预留/请求/claim 故障点和快照损坏使用隔离文件夹中的合成状态，不冒充真实断电。

`Local` 的同步调用返回前尚未暴露回执句柄。因此该窗口内 helper 死亡可能丢失
真实提交的内存回执；只承诺保留已耐久观测事实，其余保持未知/审核/配额。这不是
完整 exactly-once 外部执行、远端设备 supervisor、跨主机后端或 M4 全部验收。
真实主机重启、掉电、恶意同用户文件系统、脱离的后代进程及生产磁盘仍未验证。
当前仅支持 Linux；其他平台在状态变更前拒绝配置。

远程 Linux amd64 官方 Go 1.26.10、缓存依赖与 `GOPROXY=off`：

```sh
go test -mod=readonly -count=1 ./...
go vet -mod=readonly ./...
go test -mod=readonly -race -p=1 -count=1 ./...
go test -mod=readonly -race -count=10 ./agent/background/helper
(cd examples; go test -mod=readonly -count=1 ./...; go vet -mod=readonly ./...)
(cd examples; go test -mod=readonly -race -p=1 -count=1 ./...)
```

精确受测树、完成结果和独审记录在 PR。无新增依赖，无真实模型、设备、生产服务、
用户本机、Codex/额外计费、手动 CI、合并或部署。PowerShell 不可用，未声称执行过
Windows/macOS/PowerShell 验证。草稿 PR 保持 draft。
