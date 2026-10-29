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
