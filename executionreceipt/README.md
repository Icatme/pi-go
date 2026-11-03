# In-process execution receipts (opt-in)

This is the first, deliberately limited compatibility step toward preserving
local effect facts. It does **not** replace the full runtime/persistence work in
#144 or make the stacked filesystem write tools ready to merge.

Use `agent.RunToolCallWithReceipts` for a direct call, or
`(*codemode.Sandbox).RunWithReceipts` for standalone Codemode. Both take an
explicit maximum number of captured entries (1–4096) and return the ordinary
result plus a separate `*executionreceipt.Handle`. Retain that handle, including
when execution fails or is canceled. An invalid limit or unsupported context is
rejected before execution. Each actual executor reserves a slot before entering;
exhausted capacity rejects further entries before their bodies run.

A trusted Go executor that needs this guarantee must call
`executionreceipt.RequireCapture(ctx)` **before any I/O or side effect**, then
perform its normal current authorization checks. Immediately after an actual
local commit, call `executionreceipt.ReportLocalCommit(ctx)` and check its bool.
Capture is not authorization. The reporting API is not exposed to JavaScript.
Tools that do not call the guard retain their old behavior; this package cannot
make arbitrary existing tools safe or infer their side effects.

`handle.Snapshot()` returns independent immutable observations. A receipt has a
scope-local numeric token, parent token, domain (`agent` or `codemode`), call ID,
tool name, committed flag, and completed flag. Logical call IDs need not be
unique; use the token **within its handle**, not as a global identifier. Call IDs
and names are limited to 4096 UTF-8 bytes each. A false committed flag means
**unknown**, never “no effect”, even for an ordinary successful return. A true
flag means at least one local commit was reported, not complete success,
durability, remote completion, safe retry or a rollback guarantee. There is no
automatic retry here. A process crash between commit and report remains unknown.
No file contents, arguments, result payloads or raw errors are retained.

Bodies seal their own slot when they actually exit, before after-hooks and result
validation. Committed facts therefore survive those failures, cancellation,
projection rejection and panics after the report. Late reports on an exited
body return false. Child calls have separate slots; their commits are not
attributed to the container. A Codemode call through the Agent adapter typically
consumes three slots: Agent container, Codemode host invocation, Agent leaf.
The host-invocation receipt can remain unknown while the corresponding leaf is
committed. Static and dynamically resolved children use the same central path.
Parent tokens describe the runtime scope that admitted an invocation, not a full
cross-bridge tracing tree: a native host call and its Agent leaf are currently
siblings under the Agent container. Their domain/call IDs distinguish them.

Cancellation can return while a host invocation or child is still running.
Keep the handle and inspect later snapshots, or use `handle.Wait(ctx)` to wait
for registration to close and every admitted body to actually exit. Canceling
Wait does not terminate those bodies. Earlier snapshots do not change. A
non-cooperative body can remain pending indefinitely; host reconciliation is
required, not blind retry. Reports from background goroutines after their
executor has returned are unsupported.

## Compatibility and limits

Receipt values and non-nil Handle pointers deliberately reject JSON encoding and
decoding instead of silently becoming `{}`. This is not a persistence format.
Do not copy Handle values (they contain synchronization state); retain the
returned pointer. Typed nil pointers can still encode as `null` under Go's
standard JSON rules, and other encoders are outside this contract. Use accessors
for explicit presentation, not as an unreviewed recovery protocol.

All existing public struct layouts, `Tool.Invoke`, execution JSON, Agent events,
Codemode JavaScript errors/success values and persistence formats are unchanged.
The new facts exist only in the new host-side handle. Old consumers do not
receive them automatically; a receipt-aware host must not discard the handle
and treat an ordinary result/error as proof of no side effects. Nested receipt
wrappers are rejected; children use the existing explicit invocation scope.

This phase supports **direct calls and standalone Codemode only**. It has no
model-runner, session, journal, checkpoint, restart or durable receipt support.
Core model-runner contexts (including session/checkpoint execution) reject these
wrappers, and cannot reuse an enclosing capture capability. Starting a fresh
background context deliberately discards that boundary and is outside the
contract; trusted host code must not detach a persistent operation to bypass it.
This blocks the receipt-required tool's business side effects, not all earlier
model calls or checkpoint/session bookkeeping I/O. The existing write-tool stack
has not been replayed onto this API. Its merge
blockers remain. No default tools, filesystem capabilities, external services,
Eino dependencies, runtime locks, CAS or OS sandbox are introduced.

Future persistence support must independently preserve facts on failure and
fence unaware readers before any new write tools can rely on it. In-memory
capture and source compatibility do not prove durable recovery compatibility.
