# M6e：已验证日志的独立副本

2026-11-06，基于 M6d 草稿 #193 的 `101baeaf`。这是存储读取热路径优化，
不是检查点、日志压缩或新的数据库实现。

## 范围与正确性

M6d 发现累加主会话快照时，`Journal.Log()` 的整段 JSON 编解码占据明显时间和
分配。`agent/session.logState.apply` 已经验证并 JSON 归一化每条记录，再让读取
同一批字节通过 JSON 编解码没有增加有效的输入验证。

本次只在 `Log()` 的副本路径处理这一重复成本：

- 对已存储的 custom 记录复制 Entry、CustomData 和独立的 RawMessage 字节。
- 对 lane 记录复制 LanePointer。日志切片、顺序、Seq、ID 和 ParentID 均保留。
- 只要日志含普通 message/compaction，就整段退回原有 JSON 副本逻辑。避免
  逐条 JSON 编解码给通用消息日志增加开销；不复用 reducer 中语义不同的通用
  消息克隆，也不改变 `Entry()`、`Entries()` 或追加返回值。
- 追加和重放的验证、JSON 归一化、锁、文件同步、租约和未知提交处理完全不变。

快路径只用于已经通过 `apply` 的内部值，不接受任意调用方数据。custom 的
唯一可变部分是字节切片；它已被原来的 JSON 边界压紧、转义并处理空值，因此
直接复制不会重新解释数字或改写原载荷。仍然每次分配独立副本，没有只读视图、
共享可变缓存或依赖调用方不修改返回值的新要求。

回归测试以原 `mustCloneJSON(state.log)` 为 oracle，逐次核对 MemoryStorage 和
JSONLStorage 的全部值，并递归修改返回结果中已经填充的导出字段，再核对存储
未变。覆盖空日志、nil/empty/null/raw custom 载荷、大整数与小数字面量、嵌套
map/slice、工具 schema/采样约束/参数/回执、compaction 和 lane，分别检查纯
custom、纯 lane 和混合路径，最后严格重开 JSONL。既有协调器重放身份回归继续
检查 HistoryRef、回复、去重和私有状态。

## 测量方法

同一远程 Linux/amd64、Go 1.26.10、已有模块版本和缓存。先在精确基线
`101baeafbde0830cf297be641d709c477dc031f6` 运行 M6d 矩阵，再在本次代码运行，
各项一个操作、重复五次；本任务没有同时运行其他测试或基准。共享宿主的 CPU、
GC 和文件缓存仍未隔离，顺序运行不是随机化性能实验。

两个矩阵用相同夹具配方重新生成输入，真实时间戳让文件字节数略有差异，不能
称为相同文件。另有 `BenchmarkStoredLogCopy` 在同一进程对完全相同的已验证状态
比较原实现和新实现，覆盖 custom、message、mixed 各 100/1,000 条，防止只看
协调器 custom 日志而漏掉通用消息日志成本。

```text
go test -mod=readonly ./superagent -run '^$' -bench '^BenchmarkJournalGrowth$' -benchtime=1x -count=5 -benchmem
go test -mod=readonly ./agent/session -run '^$' -bench '^BenchmarkStoredLogCopy$' -benchtime=100ms -count=5 -benchmem
```

所有数值为五次样本的中位数，耗时单位 ms。`Restore` 已包含 `Log`，不能把两列相加当作启动总耗时。

| 负载 | 输入数 | Log 原 → 新 ms | Restore 原 → 新 ms | Log 累计分配原 → 新 B/op |
| --- | ---: | ---: | ---: | ---: |
| fixed4KiB | 100 | 10.088 → 0.088 | 18.904 → 8.835 | 3,949,432 → 541,408 |
| fixed4KiB | 1000 | 87.770 → 1.851 | 171.940 → 74.160 | 43,133,784 → 5,410,336 |
| growing256B | 100 | 34.247 → 0.652 | 72.583 → 31.220 | 15,151,400 → 2,016,736 |
| growing256B | 1000 | 2706.459 → 38.325 | 5731.682 → 2821.949 | 1,432,865,968 → 181,371,936 |

1,000 输入的 growing256B 文件为 177,526,964 → 177,528,889 字节，差异来自时间戳；打开中位耗时为 7.82 → 7.91 秒，没有声称打开验证获得优化。其 Restore 累计分配为 2,887,186,200 → 1,635,821,928 B/op；这是累计分配，不是常驻或峰值内存。

| 相同状态，1,000 条 | 原 ms | 新 ms | 原/新 allocs/op |
| --- | ---: | ---: | ---: |
| custom | 12.841 | 0.403 | 9,065 / 3,001 |
| message | 14.199 | 14.481 | 32,061 / 32,061 |
| mixed | 13.822 | 13.655 | 20,562 / 20,560 |

通用 message/mixed 路径保持整段 JSON 克隆；小幅时间差应视作共享环境噪声，不能当作通用消息性能收益。实现阶段的逐条 JSON fallback 曾增加消息日志的开销，因此没有采用。以上 final 测量均使用最终的预扫描和整段 fallback 版本。

原始记录：[精确基线](benchmarks/superagent-log-copy-baseline-20261106.txt)、[最终协调器矩阵](benchmarks/superagent-log-copy-final-20261106.txt)、[同状态对照](benchmarks/session-log-copy-comparison-20261106.txt)。基线和最终完整矩阵含 setup 分别用时 195.548 / 161.138 秒；这不是每次启动时延。


## 最终验证

在远程 Linux/amd64 上通过：

- `go test -mod=readonly -count=1 ./...`
- `go vet -mod=readonly ./...`
- `go test -mod=readonly -race -count=1 ./agent/session ./superagent/... ./cmd/pi-superagent`
- examples 独立模块的 readonly tests 和 vet。
- 定向副本回归、最终两组基准、gofmt 和 `git diff --check`。

独立源代码与测量文档审查未发现阻塞项。没有执行完整根库 race 或 Windows/macOS
测试，没有把本次结果当作真实宿主或跨主机验收。已有 SQLite 测试仍运行在临时
本地文件上；没有修改该后端实现或增加数据库服务测试。PowerShell 在本容器
不可用，使用 POSIX shell；Go 模块版本未改变，最终检查使用 `GOPROXY=off`。

## 不包含的能力

磁盘文件大小、打开时的验证、全部 transition 的恢复成本和已保留历史总量没有
因此获得上限。Log 仍需与日志大小成比例的复制内存。此实现不加速宿主自定义
Journal 或 sharedsqlite 自己的日志副本，不提供跨主机数据库方案。

检查点、归档和历史保留策略仍需遵守 [M6d 身份合同](superagent-m6d.md)。没有
新依赖、真实模型或数据库服务、用户本机、额外付费执行、CI 调度、合并或部署。
