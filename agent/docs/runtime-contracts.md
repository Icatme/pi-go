# Runtime Contracts

This document defines the stable runtime contract for `pi-go/agent`.

## Message Model

The canonical runtime envelope is [`Message`](../types.go).

- `RoleSystem` messages use `System` to append instructions, patch named sections,
  and add/remove model-facing tool declarations.
- `RoleUser` messages use `Parts`.
- `RoleAssistant` messages use `Parts`, `ToolCalls`, and provider metadata fields:
  `Provider`, `API`, `Model`, `ResponseID`, `ThinkingLevel`.
- `RoleTool` messages use `ToolResult`.
- `RoleCustom` is allowed in runtime state, but must be mapped by `ConvertToLLM`
  before model execution if the selected model cannot consume it directly.

### Parts

- `PartTypeText` carries plain text in `Part.Text`.
- `PartTypeImage` carries provider-ready base64 data in `Part.Data` and MIME type
  in `Part.MIMEType`.
- `PartTypeThinking` carries reasoning text in `Part.Text`, optional replay
  signature in `Part.Signature`, and optional redaction marker in `Part.Redacted`.

### Image Contract

- Images are accepted only as `data + mime_type`.
- Remote image URLs are not part of the contract.
- `NewImagePart(data, mimeType)` is the supported constructor for image input.

### Provider Metadata Contract

Assistant messages may preserve provider-origin metadata:

- `ResponseID`
- `Provider`
- `API`
- `Model`

These fields are part of the stable runtime shape and may be preserved through
streaming, replay, snapshotting, and resume.

### Tool Replay Contract

Assistant tool calls and tool results preserve both normalized and raw provider ids.

- `ToolCall.ID` is the normalized runtime id.
- `ToolCall.OriginalID` is the provider-native id when available.
- `ToolResultPayload.ToolCallID` is the normalized runtime id.
- `ToolResultPayload.OriginalToolCallID` is the provider-native id when available.
- Replay prefers `OriginalID` / `OriginalToolCallID` when sending historical tool
  blocks back through the built-in `pi-go` provider path.

### Thinking Replay Contract

- Historical thinking content is replayed as `PartTypeThinking`.
- `Part.Signature` is preserved during replay when the provider supports
  signature-aware reasoning blocks.
- `Part.Redacted` marks reasoning that should remain hidden from user-facing
  presentation while still preserving its runtime classification.

## Tool Hook Contract

The tool hook surface lives in [`types.go`](../types.go)
and is executed by the runtime engine in [`engine.go`](../engine.go).

### `beforeToolCall`

- `BeforeToolCallContext.Args` is mutable.
- Tool arguments are parsed and validated against `ToolDefinition.Parameters`
  before this hook runs.
- Custom parsers may return typed Go values. Validation uses their JSON
  projection while the same typed value is passed to hooks and the executor;
  custom parsers run once per call.
- Mutations made by `beforeToolCall` are revalidated before execution. Schema
  coercions are applied to the value passed to the tool body and `afterToolCall`.
- Returning `BeforeToolCallResult{Block: true}` prevents the tool body from
  executing.
- A blocked tool call is encoded as an error tool-result message and emits the
  normal tool execution lifecycle events.
- Returning an error from `beforeToolCall` prevents that tool body from running
  and becomes that call's error tool result. Sibling results remain durable.

### `afterToolCall`

- `afterToolCall` runs after the tool body returns.
- It may override fields in `Result`; omitted fields preserve the tool body's
  finalized result.
- It may override a tool-declared `IsError`, but cannot turn a returned Go error
  into success or replace the original execution facts.
- Override order is: tool body result first, then `afterToolCall` merges result
  fields and may override the error flag.
- Returning an error from `afterToolCall` becomes that call's error tool result.
  The original executor error remains in the joined Go error chain. Unprocessed
  output is discarded; already-completed sibling results remain durable.
- An explicitly configured `ToolDefinition.ValidateResult` checks the effective
  result after hooks and before final events/records. A failure becomes
  `result_rejected` and retains the executor's local/remote execution facts.

### Tool execution safety

- `ToolDefinition.ExecutionMode = ToolExecutionSequential` forces the entire
  assistant tool-call batch to run sequentially. A global sequential mode also
  takes precedence over per-tool settings.
- A `StopReasonLength` assistant message never executes its tool calls. The
  runtime appends one synthetic error result per call and lets the model retry
  with complete arguments. Failed, aborted, and cancellation-skipped calls also
  receive source-order synthetic results so the transcript remains paired.
- Truncated calls emit a start/end lifecycle with their synthetic failure, as
  required by the tool UI contract. Other failed or cancellation-skipped calls
  that never entered execution only append paired result messages.
- Tool progress emitted after `Execute` returns is ignored. Progress accepted
  before settlement completes before the tool-end event.
- `ToolResult.Terminate` is a batch hint, not cancellation. Automatic model
  continuation stops only when every finalized result in a non-empty batch has
  `Terminate=true`; steering or follow-up input may still resume the loop.
- A blocked call can opt into that rule with
  `BeforeToolCallResult.Terminate`; `AfterToolCallResult.Terminate` can override
  the final hint.
- Parameter schemas are resolved with `jsonschema-go` Draft 7/2020-12 support.
  Remote references require an explicit resolver and are rejected by this
  runtime; `format` and content annotations are not enforcement hooks. Invalid
  static schemas fail definition validation, and invalid resolver-provided
  schemas fail the run before a model request.

## Turn Boundary Hooks

For every finalized assistant turn, the runtime order is:

1. finalize all tool results
2. call `FinishTurn` with the completed `AgentTurnContext`
3. emit `EventTurnEnd`
4. stop immediately for `TurnActionEnd`; otherwise inspect continuation and queues
5. when another turn will run and the turn limit permits it, call `PrepareNextTurn`
6. recheck cancellation and pick up steering queued during preparation if no input is already pending
7. emit `EventTurnStart`, accept inputs, resolve tools and their transcript declarations
8. call `PrepareRequest` immediately before the model request

The zero decision preserves normal scheduling. `TurnActionContinue` guarantees
one more request, including after an assistant-only turn. Tool, steering or
follow-up continuation satisfies that same request. Only when no natural
continuation is available does it request again without new input.
`TurnActionEnd` leaves unread queues intact.
`FinishTurn` also observes error/aborted assistant messages and canceled tool
batches whose results committed; its decision cannot restart those hard exits.
Unknown actions fail the invocation. Suspension has no completed turn to finish.

Both turn contexts expose the invocation-local `NewMessages`, including system
declaration changes. `AgentLoopTurnUpdate` can replace context, model, model
reference, or requested thinking for the invocation. Context replacement does
not rewrite the append-only durable transcript. Preparation errors retain
accepted inputs and do not open a second turn. Already-dequeued inputs emit
message events and persist once; unread inputs remain queued.

`PrepareRequest` runs once per actual model request after input events and tool
resolution, and before transformation/conversion and provider I/O. Returned tools
are validated before the request without rerunning the resolver. The callback's
context, model and thinking are detached copies. `PeekQueuedMessages` returns a
copy of the next steering batch, or follow-up batch when steering is empty,
using the configured queue mode without dequeuing anything.

### System transcript

Initial prompt/tools seed one leading system message. Named section updates
replace or remove a section; content appends, separated by two newlines. Go
renders section names in sorted order. Before each request, executable tool
changes become declaration-only additions/removals; no executor, parser or hook
is persisted. Request-only context replacements use their own declaration
context and do not duplicate changes in durable history.

`SystemPrompt` in state/snapshots is the rendered transcript view. Update it with
`AppendMessage(NewSystemMessage(...))`; the former `SetSystemPrompt` API is
removed. `ReplaceMessages` replaces the prompt history as well. Use named
sections for replaceable instructions. Session compaction folds current system
state ahead of the retained dialogue so prompt sections and tool changes survive.
`Reset` similarly retains one resolved system baseline, including named sections
and current tool declarations. `Continue` rejects histories containing only system
messages. Existing system history is replayed wherever it begins, rather than
reseeding its rendered prompt as another initial message.
During a running invocation, use `Steer(NewSystemMessage(...))` to admit a system
update before the next request.

Preparation's explicit request prompt replacement is folded before
`TransformContext` and conversion. The resulting messages determine the request's
system prompt; removing all system messages leaves it empty. Transformations do
not rewrite durable history, and the provider adapter never restores an older
prompt over transformed messages.

The provider authoring `Context` remains available. `NormalizeContext` folds its
prompt/tools into system messages. Unsupported protocols collapse updates into
current leading state. OpenAI Responses/Completions can preserve system messages
in place when `SupportsMidConvoSystemMessages` is declared; current tools are sent
at the request root. Dynamic provider tool anchoring is not implemented.

### Structured tool results and raw events

`ToolResult.StructuredContent` is independent JSON for programmatic consumers;
`Content` remains model-facing. `IsError` can report failure without returning a
Go error and survives hooks/events/transcripts. Replacing content in an after
hook clears old structured content unless new structured content is supplied.
Invalid structured JSON becomes an error result. `OutputSchema` is declaration
metadata for ordinary tools. Approved child leaves validate successful structured
output against it; a configured `ValidateResult` also runs on ordinary tools.

`RunToolCall` shares argument validation, before/after hooks, ToolGate, current
permission checks and execution with
model-issued calls, including explicit error and structured results. It emits
no agent message events and appends no history; `OnUpdate` receives detached
progress results. Durable tool approval is still an outer runtime concern.

`ToolCallOutcome.Err` retains original errors for `errors.Is/As` and is not
serialized. `Failure` and `Execution` are serialized separately from text and
`IsError`. Remote outcomes are `not_dispatched`, `complete_reported`,
`input_required`, `unknown` or `not_applicable`; local stages are `not_started`,
`entered` and `returned`. A local cancellation does not prove remote rollback.
Physical send attempts are reported only when the transport actually observes
them. `AgentEvent.ToolErr` is a Go-only ordinary tool error, distinct from the
run/stream failure in `AgentEvent.Err`.

Only an explicitly configured container receives `ChildCaller` and the trusted
`ToolInvocation` resource holder. Its leaves are frozen for the invocation, have
host-assigned IDs and cannot create further containers. A sequential Agent policy
or any sequential leaf serializes all children in admission order. A sequential
parent alone preserves the outer model batch and does not disable parallel leaf
orchestration. Queued calls recheck permission at actual entry; executors may
recheck immediately before dispatch.

Nested gate suspension returns `nested_suspend_unsupported` without a pending
batch. Earlier child side effects remain recorded and are never replayed. Child
start/update events project identity/state before cloning, and final events use
bounded effective result summaries. All accepted calls retain basic execution
records independently of the optional 256-entry/32-KiB detail budget. Parent
outcomes/events retain the host-owned `ToolResult.ChildCalls` report independently
of raw `Details`, even when a parent hook fails. Its Go-only records are not
serialized to model JSON or durable snapshots, and executors/hooks cannot forge
them. Parent
completion closes admission and suppresses late events; active executors retain
their scheduling permits until actual return, including after cancellation.

`OnProviderStreamEvent` receives copied `json.RawMessage` and model identity
before provider normalization. Unknown fields and exact JSON numbers survive.
A callback error ends the request, preserving the cause and preventing provider
retry or transport fallback. The callback is forwarded by the default pi-go
adapter; custom `StreamModel` implementations must honor the request callback.

## Runtime mutation safety

`Agent.Reset` returns `ErrAlreadyRunning` while a run is active and leaves the
in-flight state unchanged. Abort the run and wait for idle before resetting.

### Model and thinking state

`ThinkingLevel` in definitions, constructor options, and next-turn updates is a
requested preference and defaults to `off`. `AgentState` and `AgentSnapshot`
store that preference as `RequestedThinkingLevel` and expose the model's
effective value as `ThinkingLevel`. Snapshot restoration recomputes the effective
value; a saved effective value is not a new preference. An explicit snapshot
preference takes precedence over the initial definition.

A snapshot stores only an explicit model selection. An empty model reference
continues to use the current definition's default, including after restore and
per-turn definition resolution. `Agent.State().Model` reports that effective
model; resolving a default does not turn it into a saved selection. `SetModel`
stores an explicit reference, and `SetModel(ModelRef{})` clears that selection.

The built-in pigo backend resolves supported levels from the current model
catalog. A custom `Model`, `Stream`, or `ModelResolver` preserves the requested
level by default. Set `ThinkingLevelResolver` on the definition or constructor
options to supply a pure, concurrent-safe resolver for custom capabilities.
Resolvers must not call back into `Agent`, since setters resolve under its lock.
Unknown built-in models retain the request until ordinary model resolution
reports the unsupported model.

`SetModel` and `SetThinkingLevel` commit the model, preference, and effective level
under one lock. Changing to a model without reasoning keeps the preference for a
later model switch. Changes made during a run apply at its next request and
remain authoritative when the run finishes. `PrepareNextTurn` overrides remain
local to the current invocation and do not replace the saved model or preference.
Effective thinking is recomputed after those overrides and on resumed requests.
DeepSeek receives an explicit off value because its omitted effort enables the
provider's default reasoning; other providers retain their existing off encoding.

## Runner And Event Contract

`Runner` is a stateless application boundary over `Engine`. `Run`, `Query`, and
`Continue` clone their definition, input snapshot, and prompt messages. A single
`Runner` may therefore start independent runs concurrently without sharing
mutable runtime state.

Each `RunStream` emits exactly one `EventAgentStart` and one `EventAgentEnd`.
Every event in one run has:

- a non-empty, stable `RunID`
- an `AgentName` copied from the definition
- a `Sequence` beginning at 1 and increasing by one
- a `ParentRunID` equal to the directly enclosing run when a `Runner` is
  started with that run's context

Low-level model updates are flattened onto `AgentEvent` through `UpdateType`,
`ContentIndex`, `Reason`, `Delta`, `ToolCall`, and `Err`. There is no nested
assistant-event envelope. Event values are cloned at the runner boundary so a
subscriber cannot mutate the stored result.

`RunStream.Events` is lossless and bounded. Consumers must drain it before
calling `Wait`; `Wait` intentionally participates in backpressure. `Close`
cancels the invocation, drains outstanding events, and waits for shutdown. Both
`Wait` and `Close` are safe to call repeatedly or concurrently.

Every `AssistantStream` implementation must provide `Close`. The engine selects
between model updates and context cancellation, then calls `Close` before
`Wait`. Event-loop, wait, and close failures are joined and normalized into one
terminal assistant message so message lifecycle events stay balanced.

## External Input Turn Loop Contract

The optional `agent/turnloop` child package owns a single persistent snapshot
and serializes independent `Runner` invocations. It is an application boundary,
not a graph runtime and not durable storage.

- `QueueCapacity` must be positive and bounds the shared waiting queue across
  all delivery classes; an input already handed to an active Runner is no
  longer waiting.
- `Push` is non-blocking and uses RejectNewest: a full queue returns
  `ErrQueueFull`, and any stop rejects new input with `ErrNotAccepting`.
- A successful `Push` synchronously clones the message and means admitted, not
  executed or completed. `Wait` results and unhandled inputs are independently
  cloned on every call. This matches Runner's in-memory ownership contract:
  JSON-like map values and slices, pointer-reachable exported fields, and
  cycles are detached. Map keys retain identity for stable lookup semantics,
  and mutable unexported struct internals are opaque; both must be treated as
  immutable rather than cloned through unsafe reflection.
- `DeliveryNextRun` starts a later Runner invocation and is never injected into
  an active invocation. `DeliverySteering` and `DeliveryFollowUp` use the
  engine's existing queue hooks and respect the definition's `one-at-a-time`
  or `all` mode.
- Safe delivery classes may bypass an earlier next-run input while a run is
  active; order remains FIFO within each delivery class. When idle, the oldest
  admitted input starts the next invocation regardless of class.
- An idle steering or follow-up starter forms a prompt batch according to its
  delivery class's queue mode. A steering starter then skips exactly the
  engine's initial steering poll, so `one-at-a-time` still means one input per
  model turn.
- Steering has priority if it arrives between the engine's last steering poll
  and its follow-up poll.
- The loop will not dequeue a safe-boundary input when doing so would hand it to
  an invocation that has exhausted `MaxTurns`. The input remains queued: it
  starts a later invocation after normal completion, or is returned in
  `Result.Unhandled` if a required tool continuation makes the core limit error
  terminate the loop.

The loop is the only consumer of each `RunStream`: it drains `Events` before
calling `Wait`. `OnEvent` is synchronous, serialized, and participates in
Runner backpressure. It is called without the loop mutex, so it may call
`Push`; because Runner events are buffered, a push made from an event callback
is not guaranteed to reach the same engine polling boundary. A nil callback
discards events while still draining the stream. This boundary forwards every
event produced by Runner, but cannot restore deltas discarded earlier by a
custom or built-in model-stream adapter. `OnEvent` runs on the sole loop worker
and therefore must not synchronously call `Wait` or otherwise wait for loop
termination; `Push` and non-blocking `Stop` calls are safe.

`StopGraceful` closes admission and completes active work plus every already
admitted input. `StopImmediate` may upgrade a graceful stop, closes admission,
cooperatively cancels active work, and returns inputs not yet handed to the
engine in `Result.Unhandled`. Cancellation of the parent context has the same
effective stop mode and is returned as an error. Explicit stop is normal
control flow, so a cancellation caused only by `StopImmediate` is not returned
as a runtime error. A context passed to `Wait` cancels only that waiter and does
not stop the loop; repeated and concurrent successful waits return detached
results.

Once an input is handed to the engine it belongs to the returned snapshot and
is never automatically retried, even if the invocation later fails. A Runner
error stops the loop, preserves its returned snapshot, and reports only still
queued inputs as unhandled; `Result.StopMode` is empty when no explicit or
parent-context stop became effective. Suspended initial snapshots are rejected:
pending tool approval remains the separate `agent/checkpoint` workflow.

Pushes do not interrupt an active model request or tool batch. Steering and
follow-up delivery waits for the existing core polling boundaries, and
immediate stop cannot impose a hard deadline on model, stream, or tool
implementations that ignore context cancellation.

## Tool Gate And Pending Resume Contract

`LoopHooks.ToolGate` is an invocation-local execution gate. It is not stored in
`AgentDefinition` and does not change ordinary runs when omitted.

- A gated batch is completely preflighted before any tool body or
  `EventToolExecutionStart`.
- The gate receives a detached copy of the final arguments after parsing,
  schema validation, `BeforeToolCall`, and revalidation. Gate mutation does not
  change executor arguments.
- `allow` executes normally. `block` creates a source-order error tool result
  without calling the body. Empty/unknown actions and invalid action/terminate
  combinations fail closed.
- If any call returns `suspend`, no call in the batch executes, including calls
  that otherwise would have produced immediate validation or block results. No
  tool lifecycle, result message, `EventTurnEnd`, `PrepareNextTurn`, or
  `FinishTurn` occurs.
- Suspension returns `ToolCallsSuspendedError`, keeps the assistant tail and all
  `PendingToolCalls`, stores a canonical argument view for suspended calls, and
  leaves `AgentSnapshot.Error` empty.

`PendingToolControl` binds the exact assistant call batch and its original turn
budget without using application metadata. Its digest covers source order,
normalized/original IDs, tool name, raw-argument presence and canonical value,
canonical `ParsedArgs`, and the thought signature. Zero-length raw arguments
normalize to absent; JSON round trips retain arbitrary-precision parsed numbers.

`ResumePendingToolCallsWithHooks` requires an explicit non-nil gate and rejects
a changed tail, binding, or incomplete assistant output before emitting any
event. Ordinary Run/Continue calls reject snapshots with pending tool state. A
pre-execution resume error leaves the old turn open and the exact pending state
retryable without emitting `EventTurnEnd`. A successful resume does not append
the assistant again: it executes the old batch, runs `FinishTurn`, emits its `EventTurnEnd`, and if continuation is needed, `PrepareNextTurn` before another model
turn. Re-suspension keeps the same open turn and does not reset `MaxTurns`.

## Targeted Checkpoint Approval Contract

The `agent/checkpoint` child package stores a strict, versioned
`pi-go.agent.checkpoint` envelope behind revisioned compare-and-swap:

- the initial `absent -> running` CAS completes before model or tool work
- `interrupted` contains random, single-use tool-approval interrupt IDs
- any stale or unknown decision rejects the whole resume request without a write
- a partial decision executes nothing, persists only targeted decisions, and
  rotates every unresolved interrupt ID
- a complete decision set must win `interrupted -> running` CAS before parser,
  hook, model, or tool work resumes
- approval bindings cover the checkpoint/definition domain, normalized and raw
  IDs, tool name, canonical raw arguments, and canonical final arguments
- approval or rejection is consumed on first match; a later identical call is a
  new capability request
- a changed parser/hook result invalidates the old binding and re-interrupts the
  whole batch
- a pre-commit policy/core error with the original batch still intact performs
  `running -> interrupted`, rotates all active IDs, clears consumed decisions,
  and returns the original error; a changed batch stays on the conservative
  indeterminate path
- terminal `AgentEnd` is not released until the terminal checkpoint CAS succeeds

Checkpoint runners require a non-empty `DefinitionVersion`, a fixed tool set,
and no `PrepareNextTurn` or `PrepareRequest`. `ToolResolver` and invocation-local next-turn
overrides and tool containers are rejected because their executable/model values
and live child execution state are not durable.
Custom parsers and `BeforeToolCall` hooks may run again on resume and must be
pure and deterministic.

The provided `MemoryStore` is process-local. The public `Store` contract is a
trusted secret-bearing boundary: snapshots may contain provider credentials.
A successful CAS commits the supplied payload unchanged at the next revision.
The runner reuses its validated, owned envelope after that write; records read
through `Load` still receive full format and approval-binding validation.
`running` means busy, not proof of a crashed execution, and is never replayed
automatically. A terminal CAS failure after possible side effects returns an
observational `StatusIndeterminate` outcome with `Persisted=false`; the stored
record remains `running`, so this API does not claim exactly-once tool effects.
Checkpoint streams retain Runner's bounded, lossless backpressure contract.

## Task Agent Tool Contract

`prebuilt.NewAgentTool` is an outer helper over `Runner`, not a multi-agent
runtime inside the core loop:

- its schema accepts exactly one required string field, `task`
- each invocation starts a fresh child snapshot and does not inherit parent
  transcript messages
- child events keep their own `RunID`, point to the caller through
  `ParentRunID`, and are forwarded only through transient tool updates
- the durable parent tool result contains final child run metadata and output,
  not the forwarded internal event stream
- child termination remains scoped to the child; the wrapper's final
  `ToolResult.Terminate` is always false
- child errors cancel no parent context, but they do become the ordinary error
  result for that parent tool call

The first child is at depth 1. A zero root limit defaults to depth 1; nested
tools inherit the current absolute limit, and a child may only tighten it.
`MaxTurns` is a per-child upper bound over `AgentDefinition.MaxTurns`. A tool
timeout derives a cooperative child cancellation context, so an earlier parent
deadline still wins. Model, tool, and stream implementations must honor that
context; the timeout cannot be a hard wall-clock bound over blocking code that
ignores cancellation. Child event streams are always drained before the tool
returns, including the error path.

## Structured Reflection Contract

`prebuilt.ReflectionAgent` pairs every generated draft with exactly one typed
evaluation. `ReflectionVerdict` is exactly `accept` or `revise`; summary text
does not influence the decision. A revise verdict requires at least one
non-empty instruction, while an accept verdict has none.

The model-backed evaluator accepts one strict JSON object with no unknown
fields, code fence, trailing content, or keyword fallback. Revisions replay the
complete original request, including images and prior turns, then append the
previous draft and structured revision instructions. Error, aborted, length-
truncated, empty, malformed, or cancelled generation/evaluation output is not
treated as acceptance. Reaching the configured maximum still evaluates the
final draft before returning `max_iterations`.

## Durable Session Contract

The `agent/session` child package stores finalized entries outside the core
loop. Its format and replay rules are provider-independent:

- every session starts with a versioned JSONL header and an empty `main` lane
- every entry or lane-pointer mutation receives one globally consecutive
  sequence number
- a new entry's parent must equal the selected lane's current leaf
- ids are unique and each entry has exactly one message, compaction, or custom
  payload
- memory and JSONL reads return detached values
- JSONL append returns only after flush and file sync; a failed append does not
  advance the in-memory projection
- open repairs only a non-newline-terminated final JSON syntax fragment; a
  complete unterminated item, a newline-terminated malformed item, or an
  invalid intermediate item is rejected

`Reduce` is a pure replay boundary over `[]LogItem`. `State.Branch` reconstructs
the parent chain for a lane. `State.Context` applies only the latest compaction,
using its summary and retained tail before later message entries. Summary text
remains a separate field and must be explicitly projected by the application.

Compaction never splits a turn or an assistant tool-call/result group. Missing,
duplicate, or orphan results reject compaction. If no complete older user turn
can be removed, preparation returns no plan instead of splitting the active
turn. Summary generation is supplied by a provider-independent callback and no
storage mutation happens when it fails or is cancelled.

Repository writer claims prevent two writers only within the same repository
instance. Cross-process writer coordination is deliberately not claimed by
this format. The upstream experimental operation-record/Harness protocol is
not part of this API because it is not yet converged.

## Provider Config Contract

The typed provider runtime configuration lives in [`ProviderConfig`](../types.go).

- `BaseURL`: override the provider base URL.
- `APIKey`: explicit API key or bearer token.
- `Headers`: additional request headers.
- `Auth`: typed provider auth payload for provider-specific auth flows.

### Resolution Priority

For the built-in default provider path:

1. `ModelRequest.APIKey`
2. `ModelRef.ProviderConfig.APIKey`
3. Provider environment key resolved by the provider implementation

`Auth` is only applied when the selected provider implementation supports and
needs that auth payload.

### Non-Contract Fields

- `ModelRef.Metadata` remains available for general metadata.
- `Metadata` is not the provider runtime configuration surface.
- New provider runtime settings should be added to typed config, not tunneled
  through ad-hoc metadata keys.

## Snapshot And Resume Contract

- `AgentSnapshot` is the durable runtime state shape.
- Snapshot serialization must preserve:
  - `ModelRef.ProviderConfig`
  - `Message.Provider` / `API` / `Model` / `ResponseID`
  - `Part.Signature`
  - `ToolCall.OriginalID`
  - `ToolResultPayload.OriginalToolCallID`
  - `PendingToolCall.OriginalToolCallID`
  - `PendingToolControl` while a batch is suspended
