# Session branches and journaled runs

`ReadBranch(lane)` returns a detached root-to-head snapshot and its version:
session ID, lane, lane sequence and head. `CompareAppend(version, entries)`
commits 1–1024 entries together or returns an error. Lane moves advance the
sequence, including an ABA move back to the old head. Writes to another lane
do not invalidate this version. Do not emulate this contract with repeated
`AppendEntry` calls; `AppendMessages` intentionally retains its sequential,
partial-success contract.

## JSONL v2 and writer ownership

The accepted header version is now **2**. Batch mutations occupy one physical
JSONL line: `{"kind":"batch","items":[...]}`. Entry IDs, parent chains and
sequences are validated before the line is written, flushed and synced; only
then does memory advance. A write/flush/sync error poisons the handle even if
truncating back to the previous synced prefix succeeds. Reopen to inspect the
actual durable outcome; never retry an externally effectful script automatically.

Opening a writable file locks its actual handle before loading or repairing it.
Unix uses `flock`; Windows uses a reserved byte-range lock beyond EOF. All
supported library write paths honor that lock, including path aliases and
separate repository objects. Read-only header inspection remains available.
`Close` and process exit release the lock. These are cooperative local-file
locks, not distributed database transactions.

A syntactically torn final line is discarded as a whole. A complete JSON record
without its newline is reported as corruption; malformed complete lines are not
silently repaired. Process-crash tests cover before write, partial write, missing
newline, flush, sync and after memory application. They do not simulate power
loss or promise a filesystem's persistence behavior before a successful sync.

Version 1 logs are explicitly rejected and never rewritten automatically.
Custom `Storage` implementations must add the two atomic methods. No legacy
adapter or automatic migration is provided.

## Host-owned runtime binding

Create a `Session` over storage, then prepare a fresh binding for each run:

```go
binding, err := conversation.PrepareRun(session.MainLane, session.RuntimeOptions{
    Scope: func() session.RuntimeScope {
        current := manager.Scope() // authenticated host state
        return session.RuntimeScope{Identity: current.Identity, AuthEpoch: current.AuthEpoch}
    },
})
// Handle err. Configure toolset.Options.SnapshotScope using this binding:
// saved, ok := binding.TrustedScope(snapshot)
// return mcp.Scope{Identity: saved.Identity, AuthEpoch: saved.AuthEpoch}, ok
// Configure codemodetool.Options.StateNamespace with a stable host binding name.
stream, err := binding.Run(ctx, runner, []agent.Message{agent.NewUserTextMessage(query)})
// Handle err, drain stream.Events(), then inspect stream.Wait().
```

The binding delegates to the ordinary Runner. `agent.RunJournal` provides a
generic synchronous commit boundary; `agent` imports neither this package nor
MCP/Codemode. Its canonical transcript is independent of transient
`PrepareRequest` overrides. Prompt/tool-declaration changes are saved before
the model request, and an assistant's tool intent is saved before execution.
The resulting exact branch version is used for the final commit.
When a later run first introduces a system prompt or static tools, its system
declaration is appended without changing the already committed conversation.

### Steering and follow-up during a durable run

Use `binding.RunWithHooks(ctx, runner, prompts, hooks)` to combine native
`agent.LoopHooks` with the session journal. Set `GetSteeringMessages` and/or
`GetFollowUpMessages` to the host's input source; leave `Journal` nil. A non-nil
journal is rejected before the single-use binding is consumed, rather than
silently replacing either journal. The existing `Run` is equivalent to empty
hooks. This is composition of the same Runner, not another Agent loop.

Steering is polled at the Runner's ordinary boundaries; it does not interrupt an
in-flight model call or tool batch. Follow-up is polled once steering and tools
are exhausted. Accepted messages are appended to the canonical transcript and
committed before the corresponding model request. A fresh `PrepareRun` restores
that committed history; callbacks themselves are never stored or replayed.

The host owns input admission, queue bounds, receipts, and cancellation. Retrieving
an input is **not** confirmation that it was committed: scope revocation, a hook
error or a branch conflict may stop the run. Keep input IDs/receipts at the host
boundary, inspect `Wait`, and reconcile rather than automatically replaying
side-effectful work. `RunStream.Close` cooperatively cancels and drains a run; it
does not erase already committed messages or roll back tools.

`ToolGate` and `ResolveDefinition` retain ordinary Runner behavior. In particular,
a suspending gate leaves a durable pending intent and `PrepareRun` returns
`ErrInterrupted`. This does not add checkpoint resume support; do not route the
snapshot to checkpoint resume to bypass session ownership. Reconcile actual
external effects before an explicit recovery decision.

Sequential top-level tools receive `ToolExecutionContext.State`. Codemode loads
a disposable store from it and stages a bounded versioned snapshot. A script's
state is accepted only if execution, the parent after hook and final validator
succeed without cancellation. Final tool results, child execution facts and
accepted state changes share one conditional transaction. Failed scripts retain
their execution facts but contribute no state change. Children cannot acquire
the parent's durable state capability. Late writes through a closed capability
are rejected.

The basic child-call ledger is persisted in the host-only `child_calls` field
of the `agent.runtime.v1` custom record returned by `ReadBranch`. It survives
script, hook and validator rejection and remains outside model-facing message
JSON. Optional output summaries and error details are omitted; call identities,
run IDs, execution status and failure codes remain available for reconciliation.
Canceled assistant messages and their unexecuted tool results are finalized
together, including cancellation at stream completion, without dispatching tools.
An approval-preflight service error likewise settles the entire unexecuted batch,
including allowed or suspended siblings, while returning its original cause to
the host and stopping the run. A later explicit run can use the session again.

Events report execution observations; they are **not commit receipts**. On a
commit failure the loop stops before another model request, while `Wait` returns
the final observed snapshot and error for host reconciliation. A failed commit
does not roll back a remote operation.

## Read-only acknowledgment status

`binding.CommitStatus()` returns a consistent, detached view of this binding's
last acknowledged or restored state: `Version`, `Messages`, `Pending`,
`Initialized`, `Invalidated`, and `Commits`. It checks the current trusted scope
on every read. An invalidated binding remains inspectable while scope matches;
a scope mismatch returns no transcript. Serialization failure is a read error
and does not invalidate the binding. Reads serialize with `Record` on the binding
mutex. Scope providers and custom storage callbacks must not reenter methods
on the same binding; its mutex is not reentrant.

This is **not a fresh storage read or a disk-durability receipt**. The version is
the last branch version returned successfully by storage (or loaded at prepare).
`Messages` is a JSON projection of the effective runtime prefix, not a literal
list of stored message entries: restored compaction can prepend a synthetic
summary message. Go-only fields such as `ToolResult.ChildCalls` are omitted;
JSON-compatible nested data is detached, with numbers represented using
`json.Number`. For exact stored entries, inspect `Session.ReadBranch`.

`Initialized` may come from restored runtime records. `Commits` counts only
successful `CompareAppend` calls by this binding, excluding restored baseline
and no-op records; it starts at zero for every fresh binding. Neither field is
a persistent global sequence or proof that the current input has executed.
The view does not refresh when another writer advances or moves the lane.

Keep processing and storage acknowledgment as separate dimensions:

- Input can be acknowledged before a model request fails. The input remains
  confirmed even though the run failed to produce an answer.
- A failed storage call invalidates the binding without advancing its reported
  version or prefix. The write may nevertheless have reached storage before
  acknowledgment was lost. `Invalidated` does **not** prove it was absent.
  Reopen/inspect actual storage and reconcile external effects before recovery.
- A memory-storage acknowledgment survives only in that storage object. JSONL
  uses its existing successful write/flush/sync contract; custom storage defines
  its own persistence guarantees. No API here strengthens those guarantees.

This query adds no callbacks, background polling, automatic retries, input
identity enforcement, or business authorization. Host-generated input IDs may
help correlation but are not deduplication keys enforced by the runtime.

## Restart, branches and limits

`PrepareRun` validates the saved host identity and authorization epoch against
the current scope provider. Trust uses a fresh, host-created provenance token,
not a session-ID comparison or text read from the model. The callback must be
safe to call during a run. Continue using current permission checks on actual
tools; saved data never grants permission.

Runtime records live outside the model context. Forking a lane inherits its
ancestor data, then writes independently; moving a lane requires a fresh binding.
`CompactionOptions.ProtectedTailMessages` retains a caller-identified suffix of
non-system messages, expanding to a complete turn boundary. Use it when the
current run includes host context or multiple user turns that must stay verbatim.
It combines with `KeepRecentTokens`; neither can weaken the other. A protected
whole conversation yields no plan, while negative or oversized counts fail.
The caller still owns token estimation, soft/hard request budgets, summarization
and the conditional storage commit. No default model capacity is invented.

Compaction retains system tool declarations and leaves runtime records in the
branch. Its summary is projected as conversation data for the next run, without
turning it into system authority. `toolset` reconnects and reconstructs executors
from the current permitted catalog. There is no second saved tool-selection table.

Limits are enforced on restore and commit: 64 tool/namespace slots, 4 MiB total
state, 1 MiB per serialized value, 128-byte tool and namespace names. Codemode
additionally validates snapshot version, at most 256 keys, 256-byte keys,
per-value Unicode/Number/nesting limits and 256 Ki UTF-16 units. Its envelope
preserves original JSON lexemes through outer JSON escaping. Serialization
overhead counts toward the durable byte limit. Use state for small JSON values
or resource references; put binary artifacts in the host's artifact sink.
Generic state limits apply after JSON escaping, so every accepted encoding can
be restored. Each runtime record, including its basic child ledgers, is bounded
to 16 MiB before commit.

Unjournaled existing transcripts are not automatically treated as trusted
runtime sessions. An unresolved intent causes `ErrInterrupted`; a changed scope
causes `ErrScopeMismatch`. The host must reconcile actual external effects before
making an explicit recovery decision. No automatic replay, JS stack recovery or
dynamic-container checkpoint support is added.

The [managed MCP example](../../examples/mcp-managed/README.md) demonstrates
separate-process save/resume, compaction, fresh executable declarations, persisted
JSON state and a resource reference that is authorized and read again.
