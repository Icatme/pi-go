# M5a：宿主认证的信道边界与离线协议合同

2026-11-05。基于 M4a 开发栈；开发切片，不是已合并、已发布或已接入平台。

新增可选 `superagent/channel` 包，复用 M3a 的准入日志与 M1c 的回复交付状态机。
宿主认证身份、组织、Thread、请求权限和任务关联独立于消息正文，归一化器检查
它们与协议字段一致。IM 用明确标注的模拟协议；GitHub 覆盖 PR 普通评论、已提交
评审和评审评论的部分 webhook，不用令牌、不请求真实 API。

- Source 固定为协议与组织/账户 ID 的组合，重启不能将原回复重新绑定到其他账户。
- webhook 事件身份负责去重；平台消息类型与消息 ID 负责回显和交付关联。不同
  GitHub 对象表的同值数字 ID 不混淆，回显检查在协调器锁内完成。
- 跨来源关联只使用宿主已批准的 TaskID/Thread 订阅。测试验证 IM 请求、GitHub
  更正使用同一任务历史与 Provider 会话，分别在原 Thread 回答，重投不重复执行。
- 发送只接受持久回复 ID，加载 Router 已批准内容及目的地，先领取尝试再调用宿主
  Transport。GitHub 出站仅为 PR 讨论区普通评论，不提交正式评审或审批。
- 保存消息 ID、精确尝试、可选回执游标及限流期限。游标不冒充全局分页水位。
- 限流必须确认该次尝试已结束且未发送；Source 内新尝试遵守持久期限。未知结果
  保持待核实，查询只读，无自动重发。错误或 panic 不把私密传输错误写入日志。
- 新消息身份/限流字段用新日志状态名，旧程序无法静默忽略安全语义；新程序继续
  读取旧记录。仍是单写者 JSONL，不支持降级或多进程竞争。

独立审查发现并修复两个 GitHub 身份问题：回显快照与准入之间的竞态、不同对象
类型数字 ID 碰撞。加入并发原子准入、跨对象类型、重开、未知结果、丢失回执、
回执冲突、源级限流、内容/目的地绑定、主机配置冻结和身份伪造回归。

接口、协议范围与局限详见 [信道合同](../superagent/channel/README.md)。
可执行外部包示例见 [ExampleNew](../superagent/channel/example_test.go)。

验证命令（离线、缓存依赖、Linux Go 1.26.10）：

```
go test -mod=readonly -count=1 ./...
go vet -mod=readonly ./...
go test -mod=readonly -race -count=1 ./...
go test -mod=readonly -race -count=20 ./superagent/channel
go test -mod=readonly -race -count=20 ./superagent -run 'TestReply(Channel|Cursor)'
(cd examples; go test -mod=readonly -count=1 ./...; go vet -mod=readonly ./...)
```

这些命令的最终结果记录在 PR。入站 fuzz 已执行 35,745 次且无失败。
并未运行真实平台、真实模型、手动 CI、部署或用户本机任务。

仍待后续：真实签名验证/账号连接、各平台权限与幂等/查询支持、完整 API 编解码、
更丰富消息格式、持久传输后端、邮件/文档/日程信道。宿主必须诚实提供认证与回执；
本包不是认证服务、权限授予器或网络沙箱，不声称任意平台能“恰好发送一次”。
