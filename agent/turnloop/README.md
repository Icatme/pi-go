# Bounded input and host-owned sessions

`TurnLoop` serializes the existing `agent.Runner`; it is not another Agent
implementation. Without `Config.PrepareInvocation`, it retains the original
in-memory snapshot contract, including `Config.Initial`.

For a durable conversation, prepare a fresh session binding for each invocation:

```go
loop, err := turnloop.New(ctx, turnloop.Config{
    Definition: definition,
    QueueCapacity: 16,
    PrepareInvocation: func(ctx context.Context) (turnloop.Invocation, error) {
        if err := ctx.Err(); err != nil {
            return nil, err
        }
        return conversation.PrepareRun(session.MainLane, session.RuntimeOptions{
            Scope: currentTrustedScope,
        })
    },
})
```

`session.RunBinding` implements `Invocation` directly. Do not set `Initial` in
this mode, even to a second copy of stored history: the binding owns the initial
snapshot. A nonzero `Initial` is rejected. `Result.Snapshot` is the last observed
invocation result, not a separate storage read; if the first preparation fails,
it remains zero. The host owns session storage and resource cleanup.

## Ownership and failure boundaries

- Preparation runs outside the loop mutex, before taking input. It must honor
  cancellation. `StopImmediate` during preparation leaves every waiting input
  unhandled; no invocation is started when preparation returns after the stop.
- The invocation must use the supplied Runner and preserve its control hooks;
  it may add its own journal. The Runner
  includes the loop's `FinishTurn` accounting and `MaxTurns`; replacing it or
  dropping the hooks breaks delivery semantics. This is a trusted host API.
- A start result `(nil, error)` guarantees no execution and no hook reads. The
  selected inputs are returned to their exact original queue positions,
  including an interleaved `QueueModeAll` batch. Concurrently admitted inputs
  remain after them. The loop stops; it does not retry the failed start.
- Once a stream is returned, input is handed off. A later CAS conflict, scope
  revocation, journal failure, or assistant error stops the loop without
  replaying delivered input. `Unhandled` means not delivered, **not** uncommitted.
  Inspect the final snapshot and actual storage before choosing recovery.
- Invalid invocation results fail closed. If execution may already have started
  (a non-nil stream or hook reads), inputs are not returned for automatic replay.
  Such a callback violates the contract and requires host reconciliation.
  For an invalid `(stream, error)` result, the stream is canceled and drained
  for cleanup; normal `OnEvent` forwarding is not promised on that invalid path.
- Admission, queue delivery and events are not commit receipts. The session
  binding retains its own journal, identity/authorization checks and conditional
  commits. Hooks and transient queue state are not persisted by the loop.

Steering and follow-up use the existing engine boundaries; they do not interrupt
an active model request or tool batch. Cancellation remains cooperative. This
composition does not add checkpoint approval/resume, distributed ownership,
automatic side-effect retry, or model-facing permission state.

## Trusted tool adapters

The Runner definition is reused, but each preparation creates a **new** binding
and provenance token. A `toolset.Options.SnapshotScope` or `ToolResolver` closure
must consult the current binding, not capture the first one forever. The host
can publish the freshly prepared binding through an `atomic.Pointer` before
returning it, and load that pointer in the scope callback before calling
`binding.TrustedScope(snapshot)`. Reject a missing binding or a false result.
An older binding must reject a new run's snapshot even when the session ID is
the same. Also make the authenticated scope provider safe for concurrent access.

This loop does not configure MCP, tool permissions, Codemode namespaces, or
remote effect reconciliation for the host. Current tool execution permissions
remain mandatory; a saved conversation never supplies authority. Do not share
one mutable current-binding pointer across independently running conversations.

The factory must always select the same authenticated conversation and lane for
this loop. `Invocation` exposes no pre-start session/tenant identity metadata,
so the loop cannot detect a host accidentally returning a different session's
binding. Tenant, user, session and lane routing must be validated by the host
before preparation. This interface does not automatically provide cross-tenant
or cross-session isolation; the binding's own scope checks are not a substitute
for correct host routing.

## Optional per-admission receipts

`Push` remains admission-only. `PushTracked` admits the same cloned input but
also returns a receipt unique to that admission; duplicate `Message.ID` values
are **not** deduplicated. Rejected admissions return a nil receipt and the same
queue/delivery/stopping errors as `Push`.

```go
receipt, err := loop.PushTracked(turnloop.Input{
    Delivery: turnloop.DeliverySteering,
    Message: agent.NewUserTextMessage("Use the updated amount"),
})
if err != nil { /* input was not admitted */ }
result, waitErr := receipt.Wait(ctx)
if waitErr != nil { /* this waiter stopped; the input was not withdrawn */ }
// Inspect result.Disposition and result.Err. A nil result.Err is NOT a
// business-success or storage-commit acknowledgment.
```

- `Unhandled`: input was never handed off. Its error is the preparation/start/
  stop cause that prevented delivery. A failed start without hook reads restores
  the reserved batch to its exact original queue positions, including receipts.
- `HandedOff`: a normal `(stream, nil)` invocation accepted this input, initially
  or through a control hook. This does **not** prove the model used the input,
  performed its business task, or committed it to storage. All receipts handed
  to that invocation share its final error and observed `EventAgentStart` RunID.
- `Uncertain`: the invocation violated its start contract by polling hooks and
  returning no stream, or returning both stream and error. Consumed inputs are
  not replayed or included in `Unhandled`; the host must reconcile them.

`Receipt.Wait` waits for this input's final disposition and its invocation to
settle (event drain plus stream `Wait`), **not** for the whole loop to stop.
`ReceiptResult.Err` preserves the raw invocation error, including cancellation
that an explicit `StopImmediate` can normalize to nil in `TurnLoop.Wait`.
The second return value only reports waiting/invalid-handle errors. A canceled
wait does not cancel a run or withdraw input. Completed results remain readable
by concurrent waiters, even with an already-canceled wait context. A zero-value
or nil receipt is invalid. RunID is empty if no start event was observed; cleanup
of an invalid stream+error result does not synthesize one.

Receipts retain no input or snapshot; delivered receipts share one completion
future per invocation. The loop keeps no growing list of delivered receipts.
Keeping completed receipts is the caller's responsibility. Receipts are process-
local, not durable inbox/outbox records or exactly-once execution guarantees.
Use session commit status and actual storage separately for recovery.

Do not wait for an invocation's receipt from its synchronous `OnEvent`, model,
tool, journal or hook callback: settlement requires those callbacks to return.
Handle receipts from outside the invocation or use a separately cancelable
waiter. Cancellation and shutdown still require cooperative callbacks.

Control polls check cancellation while holding the queue mutex, rather than
waiting for the parent watcher to run. If cancellation is already visible,
steering/follow-up inputs remain queued and the hook returns the context error.
This also applies to a host invocation's narrower child context: it fails that
invocation without labeling the failure as a parent stop. Inputs already handed
off are not returned or replayed. Graceful stop still permits control polling;
explicit immediate-stop normalization and raw receipt errors remain unchanged.
