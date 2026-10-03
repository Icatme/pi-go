# Pi 1.0 follow-up: shipped scope and next plan

The behavior reference is the exact upstream Pi v1.0.0 tag
(`a13d35a742c6ef8462812a28fbe1d8c8b7431c32`); the MCP protocol comes from the
already pinned official Go SDK v1.8.0. This is a Go library implementation,
with host-owned application UI, identity, permissions and network policy.

Status checked on 2026-10-03: items 1–6 below are merged and included in
`v0.12.1` / `master@dcdcf9b740190f20ace5d8bbad7a9670fe2eafd1`.
The next implementation sequence is defined in [接下来的跟进计划](#接下来的跟进计划2026-10-03).
First-round N0a/N0b/N1a–N1c are merged through [PR #21](https://github.com/Icatme/pi-go/pull/21)
and included in v0.13.0. The repaired implementation at
`992c767534ef51fbac5db3b211de5b6650653cfa` passed all six CI jobs, including
the five-platform native matrix; local review and regression evidence follow below.

| Order | Follow-up | Implemented surface | Acceptance |
| --- | --- | --- | --- |
| 1 | Useful Codemode diagnostics and discovery reference | `codemode`, `agent/codemodetool` | Syntax/source locations, allowed-name suggestions, shared BM25, namespace/result descriptions, safe errors under the effective output/header budget |
| 2 | Real raw data and SDK cache binding | `mcp.Observer`, catalog snapshots, `mcptools.FromCatalog` | JSON/SSE/stdio, shuffled requests, exact numeric lexemes, cache hits/TTL/notifications, late-response rejection, no typed/raw fallback |
| 3 | Managed connections and lifecycle | `mcp.Manager`, HTTP/stdio, process ownership, dispatch checks | Shared setup attempt, explicit reconnect, identity retirement, session/owner-exit invalidation, Windows Job Object or Unix process-group cleanup, bounded repeatable Close, no tool replay |
| 4 | Dynamic exposure and restoration | `agent/toolset`, generic `ResolveChildTools`, `NewDynamic` | Four exposure modes, lazy indirect setup, hidden discovery/guessed-name denial, search selections isolated by branch, trusted-scope resume with fresh executors |
| 5 | Explicit OAuth and credential state | `mcp.OAuth`, Manager auth/logout, memory/file stores | Issuer/state/scope handling via SDK, account/client isolation, real version CAS, restored registration/scopes, single token refresh attempt, no auth-driven tool POST replay |
| 6 | Resource list/templates/read | `agent/mcpresources`, host artifact sink | Pagination cursor, scope/permission/cancellation, bounded text/blob/images, image projection into Codemode, inert generated artifact names |

Implementation review is split by the units that can build and be verified
together: sandbox feedback and generic lazy binding; managed wire/connection/
OAuth; dynamic projection and resources. The latter units depend on the former.
Source checks and precise verification evidence are recorded in
`PO_AGENT_WORKLOG.MD`; CI executes the same contracts on five native platforms.

The deterministic `examples/mcp-managed` executable uses a real loopback HTTP
MCP server and the ordinary Agent runner. It proves that indirect schemas are
absent initially, search loads a current deferred declaration, a generated-script
fixture batches three pages and filters them in the VM, a text resource is read,
hidden metadata stays absent, and only one bounded summary reaches the caller.
It makes no model-provider request and starts no OAuth UI.

## Execution contracts

- MCP catalog schemas, scope/epoch, connection generation, revision and wire page
  digest belong to the same snapshot. The SDK owns caching; observer invalidation
  prevents late pages from becoming a new SDK cache entry. A missing observation
  is an explicit failure. Large numeric schema constraints are rejected before
  float-based schema validation can round them; raw results remain exact in Go,
  while unsafe JavaScript Numbers are still rejected.
- A script receives a fixed allowed child snapshot. Host permission and live
  connection checks run again at physical HTTP/stdio handoff. Business failures
  retain `IsError`; local rejection, ambiguous remote execution, terminal remote
  response and `input_required` remain distinct execution facts. Errors retain
  original Go causes and an explicitly safe model presentation.
- Dynamic source/header deadlines include catalog setup, subsequent permission
  checks and VM work.
  Without a run journal, successful scripts share the invocation-local store.
  Journaled runs stage branch state until the outer tool result is finalized; see
  [session integration](../agent/session/README.md). Nested
  suspension is rejected and never restarts a script with earlier side effects.
  Runtime stacks and executable closures are not serialized.
- Unix stdio ownership ends at the assigned process group. Trusted servers must
  not daemonize or use `setsid`/`setpgid` to escape it; those servers require
  host-provided external containment. Windows owns the Job Object's process tree.
- Restore requires trusted saved identity and authorization epoch. Live search
  records apply only to the exact transcript branch where they succeeded. A
  reconnect rebuilds executable definitions from the current catalog; hidden or
  removed names cannot grant capabilities. Long/sanitized MCP aliases now contain
  a namespace hash; ordinary short aliases keep `mcp__server__tool`.
- OAuth is a host action. Model tools cannot invoke a browser/login, mutate
  identity or retry an authenticated POST. Refresh with unchanged scopes may
  continue the live connection; revocation/scope change retires it. Hosts own
  credential-file ACL/keychain protection and deliberate crash-lock recovery.
- Arbitrary binary resource bytes go only to an explicit host artifact sink.
  Text and supported images remain bounded. The client does not execute HTML or
  fetch linked assets; artifacts retain scope/server/URI/MIME/hash bindings.

v0.13.0 adds durable Codemode branch/store transactions. Classifier/image model
APIs, broader grammar/provider-auth expansion and
interactive nested continuation remain outside this round. Live-provider verification
and frontend UI acceptance are separate from the local fixtures and native CI.

## Dependency and platform evidence

No dependency version is changed. MCP SDK v1.8.0 and wazero v1.12.0 are still
their latest released versions at the 2026-10-02 check. The existing OAuth and
Windows process-control dependencies become direct imports: `x/oauth2` v0.35.0
is the SDK's selected minimum; its repository remains active (v0.37.0,
2026-08-25). `x/sys` v0.48.0 was published 2026-08-31. The module requires
Go 1.26.2 and native verification uses Go 1.26.4. OAuth stays on the official SDK
path; a separate OAuth framework or native JS runtime is unnecessary.

Concurrent cold initialization reproduced wazero v1.12.0's unsynchronized version
cache ([upstream issue](https://github.com/wazero/wazero/issues/2532),
[merged upstream fix](https://github.com/wazero/wazero/pull/2536)).
The pinned QuickJS machine code is now cached for the process lifetime. Runtime
construction and cold compilation are serialized; VM execution remains parallel
with independent host imports, memory limits, state, cancellation and Close.
The upstream constructor fix is not in the pinned release, and no unpublished
dependency is injected. Concurrent-creation and runtime-isolation regressions
cover the application entry path.

## 接下来的跟进计划（2026-10-03）

### 决策与核查基线

下一轮先核对两项已有路径的正确性，再完成 Codemode 分支持久化的可用闭环。
随后处理原生 grammar 工具和动态工具声明协议。新增模型类别、更多登录方式及
交互式续接保留独立准入条件，不与持久化一起扩张。

- Go 基线：`v0.12.1`，完整提交见上；本次核查时本地与远端 master 一致。
- 行为基线：Pi `v1.0.0@a13d35a742c6ef8462812a28fbe1d8c8b7431c32`。
- 发布后补丁观察点：上游 `main@a276dabe57911253350bffb93cb7d7aff6a73261`，
  比 v1.0.0 多 25 个提交。仅筛入与现有 Go 路径有关的正确性核对；不把 main 的
  全部新功能重新定义为 1.0 必须跟进的范围。
- 验证证据：当前发布提交的 [Go tests CI](https://github.com/Icatme/pi-go/actions/runs/37087063624)
  及 Windows、Linux amd64/arm64、macOS amd64/arm64 五个平台任务成功。
  计划制定阶段仅做源码与文档分析；其后的本轮回归和实施结果见下方实施状态表。
- 根目录 `SYNC_PLAN.md` 是早期同步清单，provider 范围与未勾选状态已落后于实现；
  后续进度统一更新本文件及 `PO_AGENT_WORKLOG.MD`，不按旧复选框统计。

### 计划制定时的代码事实（v0.12.1 基线）

| 核查点 | 当前实现及缺口 | 对计划的影响 |
| --- | --- | --- |
| Codemode store | `codemode/store.go` 已有内存快照和 revision CAS；`agent/codemodetool/tool.go` 通过 `Invocation.LoadOrCreate` 建立一次 Run 的私有 store | 缺的是持久化边界与集成，不重做沙箱或再造一个内存 store |
| Session 条件写 | `agent/session/types.go` 的 `Storage` 只有单条 `AppendEntry`；`Session.State` 与后续追加不是一个条件事务；`AppendMessages` 逐条写入，允许前缀成功 | 先提供一致分支快照、比较版本和原子批次提交，不能循环追加冒充事务 |
| 分支版本 | `LanePointer.Seq` 已随 append/move 单调变化，`LeafID` 只表示位置 | 优先复用已有 lane sequence；必须识别移动后又回到原 head 的 ABA，不只比较 head，也不无故新增第二套版本号 |
| JSONL 写入所有权 | `JSONLRepository` 明确只在同一 repository 对象内排斥 writer；直接 `OpenJSONLStorage` 也没有跨句柄/进程独占 | 独占必须落在实际存储句柄层，覆盖加载、尾部修复、写入直至 Close；仅加 repository mutex 不够 |
| 提交时机 | `codemode/sandbox.go` 在脚本成功时提交内存 store；外层工具 after hook、结果校验及消息定稿发生在其后。`agent.Runner` 不负责 session 落盘 | 持久模式需暂存增量，在最终结果确定后交给同一提交边界；不能从异步事件消费者补写状态来声称原子性 |
| 工具选择恢复 | `agent/toolset` 已用可信 `SnapshotScope`、system 工具声明和当前目录恢复选择；实时搜索记录受 transcript 分支约束 | 复用既有恢复语义，补持久身份与提交集成；不并行维护第二份相互独立的“已加载工具”权威表 |
| Checkpoint | `agent/checkpoint/runner.go` 明确拒绝动态 ToolResolver、container tools 和非持久运行时 override | 分支数据恢复不等于运行中脚本恢复，本轮不打开这些禁止项 |
| Grammar | Go `pigo.Tool` 没有 constrained sampling 描述，Responses 路径没有 custom tool 流式编解码；上游仍把 custom string 映射回普通参数对象 | 单独做完整请求、流式、终态与历史回放闭环，尽量保留现有参数对象语义 |
| 模型扩展 | Go 当前只有聊天模型主路径；图片输出辅助函数不等于图片生成 API，JSON 格式回答也不等于 classifier API | 分类/图片需要独立模型类型、认证分发、结果及用量合同，不能仅往沙箱增加两个函数 |

### 下一轮执行表

以下保留原定顺序及验收目标；当前本地实施状态见下一表。N0 已先用新增回归复现。
复杂度 S/M/L 表示相对改动与验证面，不代表日历工期。

| 顺序 | 编号 / 复杂度 | 交付与文件归属 | 依赖 | 完成标准 |
| --- | --- | --- | --- | --- |
| 1 | N0a / S | 既有 Codex 登录失败边界：`internal/cli/oauth_openai_codex.go` 及对应测试 | 无 | 占用回调端口时保留根因，浏览器与 token 请求均未启动；取消不继续进入手工提示；正常回调与明确支持的手工流程分别验证 |
| 2 | N0b / S | 容量错误分类：`pkg/pigo/provider_openai_responses_shared.go`、现有 retry/stream 测试 | 无，与 N0a 独立 | 精确覆盖 `model is at capacity`；只沿现有有界策略处理首次输出前的暂态失败；`MaxRetries=0` 一次请求、配额错误不重试、输出后不重试均保持 |
| 3 | N1a / M | 原子分支存储：`agent/session/{types,storage,memory,jsonl,repository,session}.go` 及平台锁/存储测试 | 无功能依赖，紧接 N0 | 一致快照与 expected version 条件批次；同版本两次提交仅一次成功；ABA 冲突；跨句柄/进程 writer 独占；write/flush/sync 失败没有半个逻辑事务 |
| 4 | N1b / L | Codemode 分支 store 纵向闭环：`codemode`、`agent/codemodetool`、`agent/session` 集成；必要的通用定稿边界由 `agent/engine.go`、`types.go` 承担 | N1a | 同分支跨 Run/重启可读，fork 隔离；成功状态与最终工具结果一致提交；失败/取消/冲突不提交增量、不重放工具；崩溃遗留执行明确中断或未知 |
| 5 | N1c / M | 工具选择、可信 scope 与 compaction 集成：`agent/toolset`、session 集成和 `examples/mcp-managed` | N1b | 重启、切分支、压缩、重连后恢复合法选择；hidden/撤权/换账号不能恢复执行权；只从新目录创建 executor；展示完整两次进程启动闭环 |

N0a 与 N0b 可分别作为小修复交付，不依赖持久化完成。N1a、N1b、N1c 按顺序评审，
### 第一轮实施状态（2026-10-03，已合并，v0.13.0）

| 单元 | 当前状态 | 验证证据 / 限制 |
| --- | --- | --- |
| N0a | 已合并，本地与 CI 通过 | 端口占用保留网络根因并停止发布 URL/打开浏览器；登录前、OnAuth、等待及手工输入取消；真实 loopback 回调、手工码与错误 state 测试 |
| N0b | 已合并，本地与 CI 通过 | capacity 进入既有有界重试；HTTP/流内首次输出前、MaxRetries=0、配额和输出后禁重试 |
| N1a | 已合并，五平台原生 CI 通过 | ReadBranch/CompareAppend；lane Seq 防 ABA；文件句柄独占；编译后子进程覆盖锁释放和六个写入/退出阶段；JSONL v2 拒绝 v1，不自动迁移 |
| N1b | 已合并，五平台原生 CI 通过 | 通用 RunJournal + Session.PrepareRun；最终结果/状态原子提交；hook/validator/取消不保存；远端成功后落盘失败或进程死亡保留 pending 并阻止重跑 |
| N1c | 已合并，五平台原生 CI 通过 | 可信 identity/epoch/branch token、fork/move/compaction；两进程 save/resume 重建选择和重新读取资源；当前 hidden/撤权规则继续生效 |

根模块和 examples 全量 race 测试、vet、build、go mod verify 与 tidy 检查已完成本地验证。
最终审批错误修复后重跑受影响的 agent/session/checkpoint/Codemode race 回归。
[修复后实际 HEAD 的六项 CI](https://github.com/Icatme/pi-go/actions/runs/37108407584) 全部通过，
覆盖 Windows amd64、Linux amd64/arm64、macOS amd64/arm64 的 session 故障/锁测试、
Codemode/MCP race 和 managed 示例跨进程恢复。没有真实 provider 请求、UI 验收或断电测试。
公开入口、限制和破坏性变化见 [Session 集成说明](../agent/session/README.md)。
N1 的实现与五平台验收已完成，随 v0.13.0 交付；下一功能顺序保持 N2 → N3。

独立审核保留已有实现，并补充两个先失败后通过的回归：普通对话后首次引入系统提示或
静态工具不得改写 journal 前缀；OAuth 降级提示回调触发取消后不得继续手工输入。
另明确测试 v1 日志拒绝后字节保持不变。独立分支保留并审核已有实现；合并更新原工作区前，
旧的未提交版本已存入命名备份 stash。PR #21 已合并，发布与真实 provider 验收分别记录。
自动审核的四项发现也均经回归复现并修复：基础子调用记录单独持久化；状态限额按转义后
编码检查；取消后的 assistant/tool 配对完成定稿；批次去重和父链使用存储规范化后的 ID。
最终复审还复现并修复普通审批服务错误留下未配对调用的问题：整批调用明确记录为未执行，
此前允许或暂停的兄弟调用也不派发，保留原始错误并允许后续 Run 正常继续。

每个单元必须可编译、可测试；N1a 仅代表存储合同完成，直到 N1c 验收结束才将
“Codemode 分支持久化”标记完成。功能引起公开 API 或日志格式变化时明确记录，
不预先绑定发布版本号。

### N1 的固定合同与风险处理

1. **原子性归存储层。** 分支快照与 `(lane, revision, head)` 在同一读临界区获取；
   条件检查、批次写入、同步与内存状态更新在同一写入所有权下完成。
   无关 lane 的正常写入不应无故让本 lane 的提交失败。
2. **JSONL 不能伪装成数据库事务。** 当前 v1 仅有 entry/lane 单行记录。
   N1a 必须确定并验证单条事务封装或明确的新格式，覆盖进程在各写入阶段退出的恢复。
   需要 v2 时显式标注；不自动改写用户日志，不长期双写两种格式。
   只有确认现存日志有迁移需求时提供独立的一次性迁移入口。
3. **独占覆盖全部写入口。** 在读取或修复可写 JSONL 前取得实际文件的独占权；
   repository 和直接 storage 构造都遵守。覆盖路径别名、Close 释放、进程退出及错误原因。
   优先使用标准库与已固定的 `x/sys`；不为文件锁引入新的通用存储框架。
4. **数据提交与工具结果有同一个定稿点。** VM 产生待提交增量，外层 after hook 和结果
   校验确定后再落盘；事务同时保存最终工具结果、允许的状态增量及必要的执行事实。
   脚本失败或最终结果拒绝时不提交 store，但保留此前已经发生的子调用事实。
   事务成功前不向下一模型回合发布持久成功状态。
   执行前写入准备记录后，使用该次成功提交返回的版本作为执行快照版本；发生冲突时
   不重新读取最新版本强行提交。模型消息、准备记录和状态增量共用同一个提交所有者。
5. **不承诺外部副作用恰好一次。** 在可能产生副作用前保存最小执行意图/未完成标识；
   进程退出后将未定稿调用视为 interrupted/unknown，禁止自动重跑整段脚本。
   最终写盘失败时停止依赖该状态的后续回合，向宿主保留结果和执行记录。
   不把日志回滚表述为远端操作已回滚，也不引入自动恢复 JS 栈的执行器。
6. **复用现有职责边界。** `agent` 只增加确实需要的通用执行/定稿合同，不依赖 session、
   MCP 或 Codemode；`agent/session` 的集成调用现有 Runner，不另写 Agent 循环。
   `codemode` 仍是独立沙箱。N1a 评审时先固定最小接口和事务格式，避免后续各模块各造提交协议。
7. **身份与分支一起绑定。** 状态至少绑定宿主可信身份、授权 epoch、session、lane 与
   工具绑定命名空间。只有 session ID 相同不足以共享；scope 从宿主获取，不能从模型文本恢复。
   持久记录不保存令牌、闭包或可直接恢复执行权的旧 schema。
8. **压缩模型上下文不丢分支状态。** 从真实 session 分支日志恢复 custom 增量与可信选择；
   fork、移动 lane、压缩后重开均有测试。资源引用只保留必要身份/URI/hash 等绑定，
   读取时重新授权；不借 store 把任意二进制内容塞入日志。
9. **恢复仍执行数据限额。** 对记录版本、JSON 值、键数、字节数和 scope 进行校验，损坏或
   超限状态明确报错。每次 `store/load` 不重新读取整个日志；恢复一次可信分支快照后，
   只应用已确认的提交增量，分支移动或版本冲突时使快照失效。

### 第二轮与条件候选

N2 implementation is tracked on `codex/n2-native-grammar`; the reviewed contract,
exact initial capability evidence and local validation limits are in
[Native custom tool inputs](native-tools.md). N3 remains a separate, sequential
PR after the N2 draft is complete. No live-provider acceptance is implied.

N2 at `28f7ade22d1080fa892321ed686e752441664ee4` has passed independent review
and all six CI jobs in run 37119799205. N3 is now tracked separately on
`codex/n3-tool-anchors`, stacked on the unmerged N2 branch; its initial boundary
and source evidence are in [Native dynamic tool declarations](dynamic-tool-anchors.md).

| 顺位 | 编号 | 决定与理由 | 交付边界及准入/验收门槛 |
| --- | --- | --- | --- |
| 第二轮 1 | N2：原生 grammar 工具 | 计划跟进。收益是直接传递代码及 provider 约束；现有 Codemode 不依赖它才能工作 | `pkg/pigo` capability、声明编码、custom call delta/done/终态、`ctc_` ID、回放及 Agent/Codemode 接线一起交付。用单字符串属性映射回现有参数对象；缺 capability 时发送前明确选择普通 function 表示或拒绝强制 grammar，不能收到 400 再重发 |
| 第二轮 2 | N3：动态工具声明锚定 | 计划跟进。与工具搜索相关，但它和 grammar 是两个协议能力 | 分开评估 OpenAI `additional_tools`/namespace 与 Anthropic inline tool changes；先一个协议、一个可验证模型。测试增删/同名重定义/回放/缓存及模型切换，能力按精确模型声明，不按 provider 全开 |
| 条件候选 1 | N4：新增 provider OAuth | 后置于已有登录边界修复。新的 ChatGPT API token flow 与当前 Codex subscription flow 不相同 | 有明确账号/宿主集成需求后，先只做 ChatGPT OAuth；issued client ID、resource/scope、state、刷新、取消、端口冲突和凭证隔离需独立覆盖。Radius 与 Anthropic copy-code 分开排，不移植 Pi 品牌或 pi.dev 注册身份 |
| 条件候选 2 | N5：图片生成 API | 独立扩展，不借通用 `image()` 冒充完成 | 先确定一个真实 provider/model；上游 OpenRouter images 路径仅作参考。明确 image model、结果/usage、认证分发与图片字节/尺寸/输出上限，再接 `models.generateImages()`。宿主授权、并发、取消和费用观察必须覆盖模型子调用，脚本不获取凭证 |
| 条件候选 3 | N6：Classifier API | 当前没有已确认的分类工作负载，低于已有 Agent/MCP 闭环 | 先有具体任务、目标 provider 与可衡量的质量/时延/成本基线，再做一个 provider。choice/score/bool、批量结果、缺失答案、usage 缺失需有合同；不拿聊天 JSON 回答当等价实现，不顺带移植本地推理 |
| 条件候选 4 | N7：交互式 MRTR / 脚本内审批 | 最后处理；这是执行协议变化，风险明显高于普通资源读取 | 宿主先提供输入/审批 handler。MRTR 与 ToolGate Suspend 分开设计；同一逻辑调用有轮次、期限、取消、权限复核与执行台账。不得重播已执行脚本；进程退出视为中断，不承诺 JS 栈恢复 |

N2/N3 与 N1 没有硬功能依赖；安排在第二轮是为先完成一个完整主线，并减少同时修改
公共 Agent/Responses 合同的风险。N2/N3 共享 provider 编解码，按顺序整合，不抢改同一模块。
N4–N7 不阻塞下一轮交付，满足表中准入条件后再确定独立实现范围。

### 上游发布后补丁的处理决定

| 上游证据 | 当前 Go 判断 | 决定 |
| --- | --- | --- |
| [ChatGPT 回调端口占用修复](https://github.com/earendil-works/pi/commit/eeac84ca92498ac18b6832754d01aef1d3c5f654) | 上游改的是新 ChatGPT flow；Go 既有 Codex Login 也有 callback server 失败后继续发布 URL、打开浏览器的分支，现有测试甚至用 bad-address 验证降级 | 纳入 N0a 做适用性回归；不据此声称两个 OAuth 协议完全相同，第一轮已用真实占用端口复现并回归 |
| [model is at capacity 分类](https://github.com/earendil-works/pi/commit/3874b3e98983c70fa05fa193b675d42cfcb8b9f8) | Go 已识别 overloaded/high demand，但没有该短语；HTTP 503 已可重试，差异主要在其他状态和流内错误 | 纳入 N0b，保留现有禁重试条件与预算，不增加无界重试 |
| [输出循环内存上限](https://github.com/earendil-works/pi/commit/319fecb89b17b7bf8a4b62734de9a6cc6ceabe49) | Go 已有输出字节/条目、源码、桥接与堆上限及回归测试 | 保留现有更严的宿主预算，不复制上游更大的默认上限，也不重复开功能任务 |
| [Anthropic inline tools](https://github.com/earendil-works/pi/commit/b271b0a524b29e13c0c9e748aea0d34e1597f2db) | Go 尚未实现 provider 原生动态工具锚定 | 在 N3 中比较 v1.0 与新 beta 合同，避免先移植即将替换的路径；实施时核查官方支持及精确模型 |
| [MCP CIMD opt-in](https://github.com/earendil-works/pi/commit/1499466d8581035a4dec6c869725b8712aea4bbe) | Go OAuth 已接受 SDK 注册策略；Pi 的共享 metadata document 与回调 UI 属宿主身份 | 不把 Pi 的应用配置直接加入库。实际宿主有 CIMD 需求时核对现有 SDK 能力和 callback 合同后补集成 |
| Together / Cloudflare Gateway / Bedrock catalog 与 replay 修复；TUI、Nix、npm 变化 | 对应 provider/应用分发不在当前主支持范围 | 不为追平提交数量扩 provider 或移植前端；后续启用相应 provider 时纳入其准入检查 |

### 验收、依赖与执行组织

- N0 先写能失败的本地 HTTP/OAuth fixture，再修根因；浏览器 opener 使用替身，不启动 UI。
  N1 优先测 session 合同，再测 Agent/Codemode 集成。使用编译后的测试子进程验证重启与
  文件独占，不用 `go run` 代替跨进程验收。
- N1 必须覆盖：两个同版本提交、head ABA、跨 lane、跨句柄和进程、写入各阶段退出、
  parent after hook/结果校验失败、取消、远端成功但落盘失败、分支恢复、compaction、换身份。
  各修改模块 focused/race 通过后，运行根模块和 examples 的测试、vet、构建、模块检查；
  文件锁、进程与沙箱相关变更完成五个平台原生 CI。
- 真实 provider 测试与模型计费单列证据；没有真实请求时不能声明 live 验收。
  前端、登录页面及图片效果由用户人工验收，禁止 computer use 验收 UI。
- 本轮不新增库或升级依赖。2026-10-03 再查：官方 MCP SDK 最新 release 仍为
  [v1.8.0](https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.8.0)（2026-09-14），
  最近提交 2026-10-02；wazero 最新 release 仍为
  [v1.12.0](https://github.com/wazero/wazero/releases/tag/v1.12.0)（2026-05-29），
  最近提交 2026-09-28；[x/sys](https://github.com/golang/sys/commit/782b836d3ae6ebda757c7b3800818e737c623614)
  最近提交 2026-09-29。保持 Go 1.26.2 最低要求及已固定版本；这些组件未出现超过半年的维护空窗。
- 上述活跃度核查不代替 API/平台验证。任何新增或升级依赖在采用前重新检查最近发布/提交、
  Go 版本兼容、目标平台问题与替代方案；超过半年未更新提醒风险，长期未维护默认不选。
  wazero 的已知冷初始化问题继续按当前隔离措施验证，不注入未发布补丁依赖。
- 实施时 N0a/N0b 可拆独立 session，文件归属按表；N1a→N1b→N1c 强依赖串行。
  N1b 中公共 `agent` 合同由一个整合负责人拥有，避免并行定义两个接口。
  每单元完成时记录基线、变更、验证与限制到共享工作记录；不创建逐 session 完工日志。
  计划阶段仅更新文档；后续经用户授权执行本轮实现。本次审核另获提交推送授权，
  使用独立分支保存已有改动与修复；不合并或发布版本。

本轮完成的用户可观察结果应是：同一可信会话在进程重启后继续使用此前成功保存的
Codemode 小量状态与合法工具选择；切分支保持隔离；失败和不确定执行不会被自动重放。
这也是 N1 的整体完成判据。
