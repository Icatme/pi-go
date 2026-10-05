# Agent Codemode binding

`codemodetool.New` creates one sequential Agent tool with a `code:string`
parameter. The application selects an explicit leaf allowlist and chooses each
leaf's JavaScript result projection. The package does not open MCP connections,
own credentials, or create an alternative Agent runtime.

```go
codeTool, err := codemodetool.New(sandbox, []codemodetool.Binding{
    codemodetool.MCP(discovered[0], "github"),
    codemodetool.Native(localTool, "local"),
}, codemodetool.Options{MaxOutputTokens: 2000})
// Handle err, then add only codeTool to agent.AgentDefinition.Tools.
```

`Options.ConstrainedSampling` optionally requests native custom text or a Lark/
regex grammar on an explicitly supported model. Both `New` and `NewDynamic`
still execute the same `{"code": source}` argument object. Nil preserves ordinary
function tools. See [native input configuration, fallback and limits](../../docs/native-tools.md).

Ordinary short MCP names map to `mcp__server__tool`. Namespace names longer than
24 bytes, requiring sanitization, or containing the `__` separator use a capped
readable namespace plus a 12-hex identity hash. Tool names exceeding the remaining
64-byte identifier budget or requiring sanitization get a tool-identity hash
suffix. `Binding.NamespacePrefix()` lets hosts reconnect only the exact original
namespace for a saved alias; `Binding.ExportedName()` returns the complete alias.
These short SHA-256 suffixes encode stable identity, not transport integrity;
changing them would break saved aliases. Construction reuses each namespace
prefix within that catalog only. Public binding lookups and later catalog
builds derive aliases from their current names without a global cache.
Exceptional namespace aliases changed from the previous combined-prefix hash;
there are no legacy aliases. Duplicate final names
and conflicting native/MCP namespace identities fail construction. Applications
can inspect `codeTool.ChildTools` for the frozen exported names. Leaf descriptions
and schemas appear only through the sandbox discovery API. Omitted tools are
absent from discovery and execution.

The default description gives a compact globals/async/discovery reference without
preloading leaf schemas. `searchTools(query,{namespace?,limit?})` can restrict a
BM25 query to one allowed namespace. `describeTool` includes a `resultDescription`
for native text/structured values or the MCP envelope. Applications may supply
`Options.Namespaces` descriptions and instructions, available only on demand;
every entry must belong to a namespace represented in the binding allowlist.
Probe tools with `"name" in tools`; unknown member reads throw with close allowed
names, including `typeof tools.unknown`.

Every call uses the current invocation's `ChildCaller`, including validation,
before and after hooks, gate, cancellation, host-generated child IDs, and final
permission checks. Calling `Execute` without this capability fails. Leaf tools
cannot themselves declare a child allowlist. A nested gate `Suspend` rejects
with `nested_suspend_unsupported`; the original script is never replayed.

MCP results resolve to `{content,structuredContent?,isError}` built from the
**after-hook effective result**. A tool-reported MCP business error resolves;
permission, transport, protocol, schema, mapping and unsafe-number failures
reject with a machine-readable `code`, optional `reasonCode`, `callId`, and
`execution` facts. Precision rejection retains `reasonCode: "unsafe_number"`.
Original
Go error chains remain available through `errors.Is/As`. Native tools with an
output schema return the validated structured value; other native tools return
their joined text. Native business errors reject.

All visible schemas and structured results pass the safe Number boundary.
Arguments are checked both before the child lifecycle and after hooks immediately
before the original executor. Exact large Go JSON IDs remain supported by the
standalone MCP adapter; JavaScript cannot use unsafe integers as numeric IDs.
Use explicit string IDs when the service schema permits them.

Output and script options cannot widen sandbox limits. Any sequential leaf or
effective sequential Agent policy preserves VM admission order even for
`Promise.all`. The model sees bounded explicit output and one call-count summary.
Failures include a safe script/API diagnostic and source location when available;
raw Go causes remain host-only. Earlier accepted calls are never replayed and
the failure text warns that they may already have effects. Up to 1024 bytes,
capped at one quarter of the effective output cap, and one item are reserved
inside the configured/source-header limit for the summary/diagnostic. Explicit
partial output remains available on failure, and final diagnostics cannot expand
the output cap when the script fills its remaining budget.
`ToolResult.Details` contains `Report{Sandbox,Children}`; `Children` holds the
authoritative execution facts, and `Sandbox.Calls` describes bridge execution.
Reports retain partial output and accepted calls on script failure. They do not
contain leaf argument bodies, raw responses, or schemas.

Without a run journal, `store` and `load` use a private bounded store for
successive code calls of this binding in one Agent invocation. A new Agent Run
has a fresh store even when its session ID is the same.

For durable branch state, use [session.PrepareRun](../session/README.md) and set
`Options.StateNamespace` to a stable host binding name. A journaled call with
no namespace fails before running its script. Each call restores a disposable
store, stages its versioned data and lets the Agent accept it after the parent
after hook and final result validator. Final results and accepted state share
one session transaction. Failed/canceled scripts or rejected final results do
not publish state; already executed child effects remain in the recovery ledger.
Conflicts and uncertain execution are not replayed. Change the namespace when
replacing an unrelated tool environment, and take identity/epoch from the host.

`Options.Timeout == 0` uses the caller context and configured sandbox limit.
No binding-specific deadline is introduced. Script header options may only
shorten an effective host limit. `Report.Sandbox.Outputs` is omitted because
the bounded output is already retained in `ToolResult.Content`.

`NewDynamic` captures a fresh host-authorized directory for each script. Its
effective source/configured timeout starts before directory resolution; the same
absolute deadline covers the subsequent permission recheck, VM, child tools and
after hooks. An earlier caller deadline still wins. The Agent owns propagation
through `ChildToolResolution.Deadline`, without knowing JavaScript or MCP.
After valid source options have been parsed, directory, child-validation and
final-permission failures use the same reserved diagnostic budget as VM errors.
Rejected source options retain the conservative host/configured diagnostic
budget, without starting directory resolution or a VM.
Their original Go causes and rejection facts stay available to the host.

`ToolResult.ChildCalls` is the runtime-owned Go recovery ledger, attached after
parent hooks. It survives a failing parent output hook even when all unprocessed
content and `Details` are discarded. It is not serialized into default model
responses. The convenience `Details` report follows normal hook replacement and
redaction; applications use `ChildCalls` for execution facts after hook failures.

The `ToolExecutorFunc` signature now receives `agent.ToolExecutionContext`.
Application executors use `execution.Args`, `execution.ToolCall`, and
`execution.OnUpdate` rather than the previous positional parameters. MCP adapters
should use `execution.CheckPermission` immediately before sending requests.

See [the runnable local example](../../examples/mcp-codemode/README.md).
