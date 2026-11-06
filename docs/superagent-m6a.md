# M6a：共享日志、授权域租约与保守接管

2026-11-05。基于 M5a draft #169；开发切片，未合并、未发布。

## 本次边界

新增可选 `superagent/sharedsqlite` 与 `OpenMainJournal`。同一主机的多个进程
可竞争一个 SQLite 文件中某个授权域的租约；每个授权域同时只有一个有效协调器。
该协调器内部仍可并行运行不同任务。`Open`、`OpenMain`、批处理与后台工具的
原有 JSONL API 不变。新的 `MainJournalOptions` 复用同一调度器，并显式配置
普通/批量 Router、Worker 与后台后端，没有另写一套调度器。

这是 **同机替换进程** 的可验收切片。跨主机数据库、任务级租约/分片、共享写入
入口、跨实例精确控制转发、检查点、压缩与在线迁移仍待后续。SQLite 文件必须
放在支持可靠本地锁的文件系统，不得放 NFS/SMB 或多主机共享盘；不得另用其他
VFS/客户端写入。不能把本次通过的离线测试称为生产数据库或多主机网络故障验收。

## 现有 Storage 契约的核对

`agent/session.Storage` 已有 `ReadBranch` 和 `CompareAppend`。版本包含 Session、
Lane、Revision、Head，能识别 ABA；内存实现先验证整批，JSONL 将整批写成一行，
flush+sync 成功才更新内存，失败后禁止继续写。两者已有共享的重放、冲突、断写
测试。但这些方法没有持久租约，JSONL 的独占文件锁也不是可过期所有权。因此
不把它们包装成“多写者安全”的存储。

协调器实际只用 Header、Log、主 Lane 的 AppendEntry 和 Close。`LeasedJournal`
提炼这个小接口，另要求所有权 Context。契约要求每次追加将所有权 epoch、
未到期条件及预期日志头与写入放在同一事务中，不能先在内存中检查再写数据库。
读取是领取时的连贯快照；任何追加结果不明都使该句柄失效，必须重开重放。
这个适配器不是通用的 `session.Storage`，不假装支持任意 Lane/分支操作。

## 后端选型（2026-11-05 核对）

采用 [ncruces/go-sqlite3 v0.36.0](https://github.com/ncruces/go-sqlite3/releases/tag/v0.36.0)，
发布于 2026-10-29，包含 SQLite 3.54.1 与修复；Go 模块记录的提交为
`3994b44c1e91e7fb5681b2bd16c68ca448f660d5`，MIT 许可。版本要求 Go 1.26.0，
与本项目 go 1.26.2 兼容；测试环境为 Linux amd64 Go 1.26.10。依赖通过 Go
模块校验下载，锁定版本与 sums。当前驱动已使用 wasm2go，不依赖旧版的 wazero
运行方式；只导入 `driver`，不增加旧 `embed` 导入。

- 选择原因：不用 CGO，支持真实跨进程文件锁，`database/sql` Serializable
  明确映射到 `BEGIN IMMEDIATE`。本切片把平台范围保守限制为 Linux/macOS/
  Windows amd64/arm64；只有 Linux amd64 在此环境实际执行过。
- [支持矩阵](https://github.com/ncruces/go-sqlite3/wiki/Support-matrix) 和
  [VFS 说明](https://github.com/ncruces/go-sqlite3/blob/v0.36.0/vfs/README.md)
  说明锁与共享内存的平台差异。原生 Go VFS 的使用历史少于 SQLite 原始 C VFS；
  `sqlite3_dotlk`、自定义 VFS、禁用锁与内存数据库不属于本切片支持范围。
- [mattn/go-sqlite3 v1.14.53](https://github.com/mattn/go-sqlite3/releases/tag/v1.14.53)
  于 2026-11-05 发布，MIT、Go 1.21，原始 C VFS 更成熟，但需 CGO/C 编译链。
- [modernc.org/sqlite v1.61.1](https://github.com/modernc-org/sqlite/blob/v1.61.1/CHANGELOG.md)
  于 2026-10-31 发布，Go 1.26、BSD-3-Clause，是受维护的纯 Go 替代；本次没有
  另行验证它的完整平台矩阵，也不称该版本为其最新版。
- 三者核对的版本均在半年内更新，无“半年未更新”风险标记；这不替代持续维护、
  漏洞跟踪或宿主自己的部署验证。

## 租约与失败语义

SQLite 使用短的 `BEGIN IMMEDIATE` 事务、FULL 同步及有界 busy timeout。
默认回滚日志即可提供本切片的跨进程写互斥，不要求运行数据库服务或启用 WAL。
领取后持久 epoch 单调增加；Close 只按原 owner+epoch 释放，不删除或重置 epoch。
即使实例名被重复使用，过期实例也不能续租或提交新决定。

每次追加校验 epoch、owner、数据库时钟下的 deadline 及预期 sequence；在事务中
写入完整 transition，提交后才更新缓存。自动续租按 TTL/3 运行，默认 TTL 30 秒；
续租或数据库写入不确定时取消所有权 Context，协调器取消 Router/Worker。
宿主应选择能覆盖正常存储延迟的 TTL，不能把毫秒级测试参数当部署推荐。
系统时钟回拨可能延后过期判断；一旦新 epoch 取得所有权，旧 epoch 始终无效。

数据库 fencing 不能阻止已经发出的网络发送或不合作工具继续产生外部效果。
替换实例重放时，未记录终态的 Worker 进入 `needs_review`，不自动再执行；已领取
但没有可信交付结果的回复不能重发。后台操作保留原调用/投递/操作身份、已知回执
与配额，新的所有者只查询同一后端；迟到提交事实通过原操作查询核实，而不是采纳
旧协调器的新决定。宿主必须独立校验真实外部证据。

## 文件与迁移

使用宿主拥有的私有目录。Unix 目录不得有 group/other 访问权限（建议 0700），
数据库是私有普通文件（0600）；拒绝符号链接文件和任意 SQLite DSN。固定 `modeof`
绑定数据库路径以约束附属日志权限。Windows 由宿主提供等价目录 ACL。域名与实例
名是宿主配置，不是认证服务；同文件中的域隔离不能防御能直接读写数据库的主体。

`ImportJSONL(ctx, database, domain, source)` 仅导入不存在的目标域，整次事务提交，
失败无部分域/记录。先停旧实例并保持停机直到切换完成。`ReadJSONLSnapshot`
使用现有写者锁和只读句柄，拒绝断尾而不修复；旧 JSONL v2 的单条与 `CompareAppend`
批次都可导入。保留 header、entry ID、sequence、parent、timestamp 与 transition
内容，进而保留任务/Provider 身份、事件去重、投递身份、订阅、报告、回复尝试、
审核屏障和在途回执。原文件保留且不修改。

源文件锁只覆盖读取快照，宿主仍必须保证整个迁移和切换期间旧写者已停。
不要在切换后继续运行原 JSONL 文件，也不要让旧程序写 SQLite 文件。不进行
降级、双写、在线复制或自动修复损坏日志。

## 验证与仍待完成

新增失败测试先于实现；覆盖只读快照/批次、迁移各主会话提交边界、完整报告与
私有快照/订阅、未知回复屏障、失去所有权后迟到 Worker、后台效果按原调用核实。
存储包覆盖独立进程争用、进程死亡恢复、Linux SIGSTOP/接管/SIGCONT 后旧执行者拒写、
旧 epoch fencing、事务回滚、过期续租、
文件隐私与数据库/源文件验证。以下命令全部通过。独立安全审查发现路径中空格的
SQLite URI 编码问题，已修复并复验；没有剩余实现阻碍。另补最终重开数据库断言，
确认旧 Worker 不仅没有修改缓存，也没有写入持久日志。精确提交与记录见 PR。

```sh
go test -mod=readonly -count=1 ./...
go vet -mod=readonly ./...
go test -mod=readonly -race -count=1 ./...
go test -mod=readonly -race -count=20 ./superagent/sharedsqlite
go test -mod=readonly -race -count=20 ./superagent -run 'Test(MainJournal|SharedJournal)'
(cd examples; go test -mod=readonly -count=1 ./...; go vet -mod=readonly ./...)
```

没有真实模型、平台调用、生产数据库、用户本机、手动 CI、合并或部署。未验证
真实断电、文件系统损坏、跨主机网络分区、Windows/macOS 运行时与大规模日志性能。
日志目前全量重放且保留缓存；检查点/压缩需后续单独验证，不能绕过审核屏障。
