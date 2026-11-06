# M7b · 耐久设备链路组合验收

2026-11-05。基于未合并 draft #181 的精确树
`385244ee346edf836c1ff7dcbba0ec029d82df37`。本切片复用现有 M6a 同机租约、
M7a HTTP 设备协议和 M4c 耐久 helper，只增加组合回归与文档，未新增后端、
修改生产 API、共享日志格式或依赖。它不是 M7 全部完成或真实设备上线。

## 组合边界

外部包测试 `superagent/device/durable_*_test.go` 通过真实 loopback HTTP 串联：

1. 测试宿主持有 `sharedsqlite.Journal` 和 `SuperAgent`，保持原操作、任务、
   调用、投递与完成事件身份，并在 unknown 时保留审核和配额。
2. 独立 endpoint 子进程使用 `device.Create/Open`，后端是现有
   `helper.Create/Open`。两个持久注册表只在首次明确创建，其后严格 Open。
3. helper 使用同一个测试可执行文件的固定入口运行 `helper.Run`。它在 endpoint
   进程之外生存，不继承 endpoint 的取消上下文或环境。测试副作用仅为临时私有
   目录中的标记，不操作真实设备、账户、业务文件或外部服务。

三层配置必须匹配，不能把设备授权 scope 当成本地 workspace：

```go
binding := device.Binding{
    Domain: "project-domain", Device: "selected-device",
    Workspace: "native-workspace", PolicyRevision: "v1",
}
names := []string{"work"}
// helper.Options.Scope and helper.NativeConfig.Scope = binding.Workspace
// helper.Options.PolicyRevision and helper.NativeConfig.PolicyRevision = binding.PolicyRevision
// device.Config = {Binding: binding, Tools: names, Backend: durableHelper, Authorize: deviceAuthority}
// device.ClientOptions = {Binding: binding, Tools: names, URL: exactEndpoint, Transport: authenticatedTransport}
// superagent.BackgroundTool.Scope = device.Scope(binding, names) = client.Scope()
// superagent.BackgroundTool.PolicyRevision = binding.PolicyRevision
```

这是配置关系，不是可直接部署的认证示例。宿主必须提供固定可信二进制、实际
权限校验、认证且不重试的 transport、TLS、资源限制和可靠本地持久存储。

测试认证服务只桥接当前 M6 Journal 的 Context/Epoch，每个客户端固定原拥有者
epoch；旧客户端不能通过读取可变“当前 epoch”冒用新拥有者。设备与 helper
后台命令分别检查该当前授权，原生 helper 始终以 request manifest 的原始 epoch
检查权限。新主可以查询/停止旧操作，但不能为旧 helper 自动换发执行授权。

本测试只验证明确 Close/reopen 换主。Context 失租通知是异步的；该 loopback
服务和自定义头不是生产认证协议、分布式租约 oracle 或原子外部效果 fencing。

## 已覆盖的实际组合行为

- 正常确认及启动后丢 HTTP 确认：一次物理原生进入；重复查询原 ID，不自动重发
  Start、不换设备/ID。正常启动后同 Worker 仍可接收后续输入。
- endpoint 进程被终止后，同一 helper 继续等待；重开两个注册表只能给出 unknown，
  不能把陈旧 running 当作当前存活。释放原 helper 后可取得终态。
- helper 已提交并返回、endpoint 已持久化终态但 HTTP 查询回执丢失：重开后按原 ID
  恢复提交/退出事实，仍只有一次 helper 启动和一个协调器完成事件。
- M6 明确换主后，新主首次访问设备之前，旧客户端的 Describe/Start/Inspect/Stop
  都拒绝；新主查询原 ID。原生 pre-effect CheckPermission 使用原 epoch 并拒绝，
  不因新主的查询而获得权限。
- 接令前断线、device 已预留但尚未调用 helper、helper 已启动但 device 未记确认、
  helper 记录内存提交后退出：未知结果与持久配额不变；辅助进程死亡不证明无效果。
- 停止请求发送前/确认后断线、device 落盘停止意图后/helper Stop 前崩溃、helper
  Stop 后/device 回应前崩溃：M4 重启 watcher 重投原 ID 的 Stop，审核与配额保留。
- 直连 M7 的 Inspect 不补送尚未到达 helper 的 Stop；调用方须保留停止意图并明确
  重复同 ID Stop。M4 watcher 已提供此恢复行为，没有另加隐式写操作。
- stop-before-start tombstone 跨 endpoint 重启后永久阻止该 ID，仍为 unknown，
  不伪造退出。已终态后重复 Stop 不擦除提交事实。
- 独立模拟取消后仍运行的 native body：停止/撤权不等于退出，迟到提交保留，实际
  返回后才有 completed 回执；终态后可显式审核。恢复只生成一个原操作完成事件。
- 设备私有结果 canary 确实出现在 helper 的私有快照中，但不进入设备 HTTP、设备
  注册表、协调器日志、Worker 输入/快照、报告、回复或后台公开完成内容。

敏感输出测试不授权在普通参数中传递凭据。M7 Start 参数本来就在 wire 中，helper
会持久化其请求；凭据应由 native 宿主闭包管理，不能放入参数、固定命令行或日志。
本切片没有扩大原始结果公开范围。完成只证明投影后的提交/退出事实，不证明
业务成功、回滚、外部 exactly-once 或所有崩溃前提交都已捕获。

## 验证

远程 Linux amd64、官方 Go 1.26.10、只读缓存模块、`GOPROXY=off`：

```sh
go test -mod=readonly -race -count=3 ./superagent/device -run TestDurableChain
go test -mod=readonly -count=1 ./...
go test -mod=readonly -race -p=1 -count=1 ./...
go vet -mod=readonly ./...
(cd examples; go test -mod=readonly -count=1 ./...)
(cd examples; go test -mod=readonly -race -p=1 -count=1 ./...)
(cd examples; go vet -mod=readonly ./...)
```

最终受测树、各检查结果与独审结论记录在 draft PR。独立检查完整 module graph
的结果另列，不把使用缓存成功编译等同于所有传递版本元数据齐全。

无真实设备、真实模型或生产服务调用，无用户本机、Codex/额外计费执行、CI 触发、
合并或部署。未覆盖真实跨主机网络、TLS/认证、主机重启/断电、恶意文件系统、
Windows/macOS、设备选取/等待重连调度、原始结果/制品传输。实际独立进程崩溃
证据不能替代这些验收。
