# M6b：可选单主 PostgreSQL 日志切片

历史记录。2026-11-06 的存储边界已取代本切片在根库中的实现。
根库提供公开的 `Journal`、`LeasedJournal` 与同主机 SQLite，PostgreSQL 由宿主自行实现，
通过 `OpenMainJournal` 接入。下文保留当时的实现与验证范围，所述包、驱动依赖
和测试入口已经移除。当前契约见 [SuperAgent 接入说明](../superagent/README.md)
和 [`LeasedJournal`](../superagent/journal.go)。

2026-11-05。基于 #178 HEAD `6292543b231b3ee8287e7de2598ff33f8bafa3ab`，
基线 tree `3fd1dd4058b1cd362b1972e591527062d6ab1a21`。仍是未合并开发切片。

新增 `superagent/sharedpostgres`，复用 `LeasedJournal` 和现有调度器。宿主提供
`database/sql` 连接及一次性 schema 迁移；包不读连接串/凭据、不自动建表、不关闭
宿主连接。保留单授权域单所有者，域内并行调度不变，没有扩展为多主/任务分片。

每次事务取得域行锁后才单独采样 `clock_timestamp()`，不采用 PostgreSQL 的事务
起始时间。持久 epoch 和 seq 在同一事务中 fencing，重放/写入后提交前再次校验。
续租提交前仍须处在原授权期限内，新的本地单调期限从请求开始计时，不能从 ACK
时刻重新加 TTL。独立截止计时器不等待 I/O 持有的事务门闩；迟到 ACK 不复活句柄。
客户端事务 Context 和事务级 statement/lock/idle timeout 有界，明确要求驱动遵守
取消。所有 COMMIT 错误/过晚回执按可能已提交处理，关闭所有权，不盲重试。

日志使用原始 `bytea`，JSONL 只读导入保留 header/LogItem 字节及现有回执/审核屏障。
迁移只允许不存在的域，事务失败不留下部分导入；源写者须在整个切换期间保持停机。
原文件不修复、不删除。当时的协议、宿主权限及限制保留在
[M6b 原始 PR](https://github.com/Icatme/pi-go/pull/179) 中。

## 验证边界

先添加离线故障合同测试，基线因 API 缺失编译失败后实施。脚本驱动覆盖锁后时钟、
提交前过期、epoch/owner/seq、忙锁、溢出、COMMIT 未知、I/O 阻塞期间取消、
迟到续租、原期限重验、只读/批次原字节迁移和事务回滚。脚本不是 PostgreSQL 模拟器。

真实 PostgreSQL 集成测试必须显式双环境变量门控，默认跳过。提供独立连接池争抢、
旧所有者/同名 ABA fencing、真实行锁超时、服务器错误导致事务中断、注入丢失
COMMIT 回执，以及迁移字节/回滚用例。注入丢失回执不等于真实网络分区。

**本环境没有可用 PostgreSQL 服务/Docker；真实 PG 用例未运行。** 不安装/启动服务、
不借用凭据、不连接生产。不能据离线成功声称跨主机、网络故障或生产可用。PR 保持
draft。单主数据库故障转移、独立进程暂停/死亡、TCP 分区、数据库重启、真实磁盘
持久性以及两台主机验证仍待已有授权的隔离环境。

测试依赖 [pgx/v5 v5.11.0](https://github.com/jackc/pgx/releases/tag/v5.11.0)
来自官方 Go 模块（2026-09-07 发布），MIT，要求 Go 1.25；运行时包不导入
驱动。固定间接依赖 pgservicefile 的版本年龄超过半年，需持续跟踪维护/漏洞，
本次没有完成独立漏洞扫描。宿主应显式配置 TLS、连接角色、同步提交及持久存储。
异步副本提升或从旧备份恢复可能回退 epoch 和去重事实，不在安全前提内。

所有执行在现有远程 Linux/amd64 Go 1.26.10 环境。PowerShell 不可用，未声称
Windows/macOS/PowerShell 验证。不使用本机、Codex/额外计费、真实模型/平台操作、
手动 CI、合并或部署。精确受测树、测试命令、独审和远端提交记录在本切片 PR。

以下验证命令仅适用于该历史 PR 的代码与依赖。
当时禁用所有 live 标志，并在模块缓存就绪后使用 `GOPROXY=off`。

```sh
go test -mod=readonly -count=1 ./...
go vet -mod=readonly ./...
go test -mod=readonly -race -p=1 -count=1 ./...
go test -mod=readonly -race -count=20 ./superagent/sharedpostgres
(cd examples; go test -mod=readonly -count=1 ./...; go vet -mod=readonly ./...)
(cd examples; go test -mod=readonly -race -p=1 -count=1 ./...)
```

独立只读审查未发现可复现阻碍，并另行通过新包离线 race×5 与 vet。examples
模块只同步 `golang.org/x/text` 的选定间接版本及校验和，避免新增测试依赖后
`-mod=readonly` 失效；没有更改 examples 源码。
