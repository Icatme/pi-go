# M1b: structured main control and execution boundaries

This slice builds on [M1a](superagent-m1a.md). The main session can choose
`new`, `attach`, `steer`, `cancel`, `reply`, `clarify`, or `ignore`. The Worker
scheduler remains shared with the legacy API. No external transport, live model
service, new execution backend, or additional Worker scheduler is introduced.

## Go host contract

- `MainJob.Runs` contains detached, exact `RunTarget` values for currently
  controllable invocations. Unlike the 128 recent task summaries, this list
  includes older active tasks too; the concurrency limit bounds it to 64.
- For `steer`, return `MainDecision{Action: "steer", Target: &target, Text: text}`.
  The text is 1–4096 UTF-8 bytes and must not be blank. For `cancel`, return
  `MainDecision{Action: "cancel", Target: &target}` without text or `TaskID`.
  Copy a complete offered target. A task ID alone cannot select an execution.
- Only a main `user` input classified by the trusted host as `request` may
  select a control. A signal or Worker result cannot authorize one. The
  coordinator retains its own offered-target set, rejects invented/modified
  targets, and rejects targets that exited or were cancelled during inference.
- `clarify` uses the same durable reply outbox and original input destination as
  `reply`. It cannot create work. Worker results remain restricted to `reply`
  or `ignore`; generated output never expands its own authority.
- `MainControls()` returns detached durable action records. Each has a stable,
  main-session-scoped `ID`, originating `InputID` and `DecisionID`, the original
  request's `Source`, `Thread`, and `EventID`, and the exact execution `Target`.
  Deduplicate by action ID and render its actual status, not a model's claim.
- A control decision consumes the original input and advances the main snapshot
  but does not itself generate a reply. Hosts expose `MainControls` to users;
  the Worker's terminal report returns to main through the existing result
  inbox. Control delivery-status inputs and automatic clarifications after an
  unissued control are not introduced here.
- `WaitIdle` includes pending control execution as well as input/main work. It
  does not wait for external delivery acknowledgement. Native `Steer` and
  `Abort` remain exact-invocation controls, available while main inference is
  blocked. Native steering remains transient; repeated native calls are not
  deduplicated. Durable main controls share the same 16-entry Worker mailbox.

Each journal is one host authorization domain. Host adapters must authenticate
requests and use separate journals for separate domains. Callback code is
trusted Go code, not a sandbox. `PiMainRouter` remains tool-free and now includes
exact running identities in its bounded model context. Its JSON parser rejects
unknown, duplicate, case-aliased, missing, null, or incorrectly typed target
fields, including nested objects.

## Commit, dispatch, and delivery

One `main_decided` append atomically records the consumed input, validated
snapshot, structured decision, and pending action identity. No Worker control
is issued in inference or before that append succeeds. The scheduler then
rechecks the exact invocation and records its next state before acting:

| Status | Meaning |
| --- | --- |
| `pending` | The validated decision is durable; the scheduler has not issued it. |
| `queued` | Steering was admitted to this invocation's mailbox, not delivered. |
| `delivered` | A Worker polled the instruction at an input boundary. It does not prove the model consumed it or a tool acted on it. |
| `cancel_requested` | Cancellation was recorded for this invocation. It is not evidence that an executor stopped or rolled back effects. |
| `not_delivered` | The action was not issued or the instruction was dropped without a durable poll, including a full mailbox, stale target, Worker exit, cancellation, or restart. |
| `needs_review` | A delivered instruction belongs to an interrupted invocation after restart; do not infer successful consumption or replay it. |

`QueueModeAll` persists all durable entries in one append before returning any
message, including when native and durable instructions are mixed. Failed
appends return no messages and fail-stop the coordinator. Native receipts are
still released. Fatal cleanup does not recursively attempt more journal writes.
A late-success Worker after cancellation retains its observed snapshot and tool
receipts, reports `interrupted`, and leaves the task behind the review barrier.
The Worker slot is retained until it actually returns.

## Recovery and migration

A missing Worker terminal record still becomes `interrupted` and
`needs_review`. On reopen, pending/queued controls become `not_delivered`;
delivered controls against an interrupted invocation become `needs_review`.
A durable `cancel_requested` record remains a cancellation request, never a
completion receipt. Its original Worker's report determines the outcome.

No control is automatically reissued after restart, and none can spill into a
later event on the same task. Reconciliation does not replay old controls or
clear uncertain execution facts. After checking effects, a host can submit a
fresh authorized instruction with a new event ID. Duplicate `(source,event_id)`
input remains a no-op, including after restart.

Existing M1a journals replay without modification to identity or completed
work. New controls add `control_*` transitions and new decision variants; older
binaries cannot read them. Stop the single writer and keep a backup before
upgrading; do not roll a journal containing these records back to M1a binaries.

**M2 boundary:** M1 still has a single-task binding per source/thread. An input
from an already-bound conversation follows the existing Worker fast path; it
does not automatically become a main steering/cancel decision. This slice is
validated using an unbound operator conversation. Same-chat classification,
persistent correction routing across runs, snapshot-based confirmation of
consumption, and multi-topic/many-to-many subscriptions remain M2 work. The
external-package `ExampleSuperAgent_MainControls` demonstrates this boundary.

## Verification

The first test-only commit adds `TestMainStructuredSteeringIsDurable`; on M1a
it fails to compile because `Runs`, `Target`, and `MainControls` do not exist.
The implementation makes the same acceptance green. All models are offline.

Coverage includes atomic decisions, every control commit prefix, duplicate
admission/reopen, an interrupted delivered instruction, the same task's next
execution, stale targets before/after commit, callback mutation, signal/result
permissions, nested JSON validation, queue limits, mixed native/durable batches,
one-at-a-time polling, native abort, successful exit/Close drops, append failure,
late committed effects, old active tasks, and detached host reads.

All commands below passed on 2026-11-05 in the Linux amd64 cloud workspace
with official Go 1.26.10. An independent review found and corrected the active
run/history-window issue; focused M1b race tests passed 20 repetitions. The
one-at-a-time and Worker-exit/Close tests also passed 20 race repetitions.
`gofmt` and `git diff --check` passed.

```sh
go test -mod=readonly -count=1 ./superagent
go test -mod=readonly -race -count=1 ./superagent ./agent/session
go test -mod=readonly -count=1 ./...
go vet -mod=readonly ./...
# From examples/:
go test -mod=readonly -count=1 ./...
go vet -mod=readonly ./...
```

Prefix fixtures simulate process loss at durable boundaries. Existing journal
and Worker process/torn-write tests remain the underlying filesystem coverage.
No manual CI dispatch, external send, live provider, deployment, or Windows/macOS
validation is part of this slice.
