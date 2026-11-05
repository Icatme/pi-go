# M6c：PostgreSQL 协调器门控验收

基于 draft #185 HEAD `1b04bc6f20984d9f1fbde93eea490f7334e81978`，
基线 tree `887728b266b265e17f3e52673f918d1255deb02e`。仅新增测试及说明，
不改运行时代码、依赖、日志格式、生产 schema 或 CI 配置。

## 覆盖目标

原有八个真实 PostgreSQL 用例主要验证存储协议。本切片新增五个
`TestPostgresCoordinator*` 测试入口，把 PostgreSQL journal 接到真实
`OpenMainJournal`，使用进程内确定性 Router、Worker、后台和平台回执 fixture：

1. 停机 JSONL 在每个已接收主流程提交边界导入独立随机域。核对源文件不变、
   事件去重、原任务/决策/回复关联和 Worker 次数；未结束的 Worker 保持
   `needs_review`，已认领的回复不可重复发送，已验证回复保持确认。
2. 两个任务的私有快照、订阅、报告及原回复 attempt 迁移后不变。一次订阅回调
   恰好进入两个原任务；新连接池重新打开后去重、报告、回复及日志保持一致。
3. 暂停旧 journal 的测试续租循环，等待真实所有权截止，随后使用独立连接池
   接管。旧 Router 的迟到决策、旧 Worker 结果及新事件接收不得更改持久日志。
   未提交 Router 输入恢复一次；运行中 Worker 不重跑，保留原 delivery 审核关联。
4. 回复认领实际 COMMIT 成功后，仅注入客户端 ACK 丢失。旧宿主不得得到发送
   句柄；新宿主只恢复原未知 attempt，不重跑 Worker、不自动重发。旧宿主的
   迟到回执被拒绝，新宿主的可信原 attempt 观察在再次打开后保持确认。
5. 后台操作从 JSONL 迁移后未知，原 operation/call/delivery/授权域身份不变。
   注释不能解除审核，后续任务暂缓执行；可信的原操作迟到退出与 committed
   receipt 只触发一次私有完成输入。再次刷新/重新打开不重复 Start 或完成。

## 门控与安全边界

所有五个入口首先复用 `postgresFixture`，只有宿主明确同时提供下列两个值
才会解析连接配置或创建任务。默认在入口 `t.Skip`，不搜索账号，不读取其他
PG/用户目录默认凭据，不连接已有业务数据库，不创建账号，不安装或启动服务。

- `PIGO_POSTGRES_TEST_DSN`：宿主已授权的**可丢弃专用** PostgreSQL URL。
- `PIGO_POSTGRES_TEST_DISPOSABLE=1`：明确允许该测试数据库的 fixture 写入。

这些值是操作者的环境配置，不是让测试自动识别或保证数据库可丢弃。连接前必须
确认目标确实专用且不含业务数据。测试沿用固定 `pi_superagent_v1` schema 的
显式初始化/结构检查，仅清理自己随机生成域的两张表行；不 drop schema、
不删源 JSONL、不清空其他域，不修复未知表结构。远端要求 `verify-full`；
仅显式 loopback 可使用 `disable`。不得启用这些门控绕过服务使用授权。

没有真实 PostgreSQL 服务参与本次执行。五个入口仅编译并明确 **SKIP**，
不能据全仓离线测试通过声称协调器 PostgreSQL 验收通过。即使未来在专用服务
运行成功，也只证明本地进程内回调配合单主 PostgreSQL 的这些合同。

## 复现

先确认缓存与 Go 版本，离线检查时显式关闭所有 live 标志及数据库门控：

```powershell
$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:GOTOOLCHAIN = 'local'
$env:PIGO_LIVE_TEST = '0'
$env:PI_GO_AGENT_LIVE_TEST = '0'
$env:PI_GO_AGENT_EXAMPLE_LIVE = '0'
$env:PIGO_POSTGRES_TEST_DSN = ''
$env:PIGO_POSTGRES_TEST_DISPOSABLE = ''
go test -mod=readonly -count=1 -v ./superagent/sharedpostgres -run 'TestPostgresCoordinator|TestPostgresFixtureRequiresBothGates'
go test -mod=readonly -count=1 ./...
go test -mod=readonly -race -count=1 ./...
go vet -mod=readonly ./...
Push-Location examples
go test -mod=readonly -count=1 ./...
go test -mod=readonly -race -count=1 ./...
go vet -mod=readonly ./...
Pop-Location
```

未来操作者另获明确的专用数据库使用授权并自行配置双门控后，可运行：

```powershell
go test -mod=readonly -count=1 -timeout=5m -v ./superagent/sharedpostgres -run '^TestPostgresCoordinator'
go test -mod=readonly -race -count=1 -timeout=10m -v ./superagent/sharedpostgres
```

测试不会替操作者准备服务或凭据。本次只使用现有远程 Linux/amd64 与缓存的
Go 1.26.10，PowerShell 不可用，因此实际命令以 Bash 对应形式执行。
没有本机、Codex/额外计费执行、真实模型/平台操作、CI 触发、合并或部署。
精确受测树、独立审查与最终离线检查结果见本切片 draft PR。

## 仍未覆盖

这些 Router/Worker 是固定回调，不是真实模型或 Pi 推理质量验证；后台工具
只记录内存事实，平台 receipt 由 fixture 注入，没有外部恰好一次保证。ACK
丢失是客户端返回值注入，不是真实 TCP 分区。独立连接池不是独立进程/主机。
这里的重启是协调器/journal 重开，不是 PostgreSQL 服务重启。跨进程 kill/pause、
跨主机、真实网络中断、数据库重启/故障转移、掉电/磁盘持久性、生产权限与 TLS
配置，仍需另外授权的隔离验收。该 PR 保持 draft，不能作为部署或发布验收结论。
