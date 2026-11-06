# M4a acceptance contract: controlled background tools

This is the first M4 development slice on top of M3b. It is not a released
service, durable process runner, remote device backend, or completion of M4.
All execution in its acceptance fixtures is offline, in-process Go code.

## Failure acceptance established before implementation

- Hold a registered tool behind a channel barrier. Persist operation identity,
  original task/delivery/native call, host scope/policy revision and independent
  quota before dispatch or returning Started. The same Pi Worker must process
  another input before the barrier releases.
- Preserve exactly one native tool response for the launch. Deliver completion
  once as a new input to the original task, retaining the original call ID even
  if subscriptions or later inputs change. No second tool result is injected.
- Stop before launch acknowledgment, then let a cancellation-ignoring body
  commit. Preserve stop intent, actual exit and committed receipts separately.
  Only actual exit releases quota. Worker Abort is not operation Stop.
- Restart at each journal boundary after reservation. Query the original ID,
  never replay Start. Missing backend evidence retains the review barrier and
  quota. Reusing a still-live Local instance permits observation to resume.
- Denied, expired or cross-task capabilities, unregistered tools, full quota,
  invalid arguments and forged completion sources do not cross dispatch.
- Partial committed facts survive unknown and stale observations. Regressing
  receipts do not release quota. Large receipts cannot break completion
  admission after its terminal record has already been appended.
- Raw arguments/output stay private. Public reports carry authoritative result
  facts, detached from host/model mutation. Replay does not duplicate delivery.

## Host API and package boundary

`agent/background.Backend` is the generic execution lifecycle interface:
`Start`, `Inspect`, `Stop`. `NewLocal` adapts an explicitly supplied native tool
allowlist through `agent.RunToolCallWithReceipts`. It owns a host root context,
not a Worker-run context, and holds its own capacity until all captured native
bodies actually exit. Ordinary tool execution, hooks, schema validation and Pi
protocol behavior remain in `agent`; orchestration stays in `superagent`.

`OpenMainBackground` adds an explicit `BackgroundOptions` configuration to the
single-input main router. Each registered tool requires a name, non-secret
scope identifier, policy revision, backend and current-authority callback.
The callback receives coordinator-owned task/run/delivery provenance and a
copy of the requested arguments. A model cannot select scope, policy, backend,
quota, task or original delivery. Native backend permission callbacks still
apply, including a tool's own check immediately before its effects.

`Job.Background` offers scoped Start, Inspect and Stop methods only while that
Worker invocation is active. `PiBackgroundWorker` opts into native
`background_start`, `background_query` and `background_stop` tools alongside
ordinary synchronous host tools. It retains native permission hooks and
rejects reserved name collisions. Registered launch names appear in the
schema. Query reads the latest durable observation, not a forced backend call.

Every launch receives a new coordinator-generated operation ID. The provider
call ID is correlation data and may repeat across turns; it is never a global
idempotency key. Backends must deduplicate their immutable operation ID and
must never execute it again with different contents. Start is not retried.
The persisted quota reservation precedes dispatch. Dispatch error/panic is
uncertain, not proof of no effects, and retains quota pending verification.

Host APIs `BackgroundOperations`, `RefreshBackground`, `StopBackground` and
`ReconcileBackground` inspect, query, stop or review exact operations.
`BackgroundOperations` includes private arguments/output; protect it like
`TaskSnapshot`. `BackgroundCompletion` is attached privately to the owning
Worker's new input. `Report.Background` carries a separate output-free factual
view so generated prose cannot erase a stopped or failed operation's receipts.

## Stops, uncertainty and completion

A durable stop intent is independent of a Worker Abort. It is retried using
the backend's idempotent Stop contract after restart. Neither a stop response,
context cancellation, native invocation return nor exit-code claim releases
quota. The local backend waits for the captured executor bodies to exit.
Completed receipts mean those Go bodies exited, not that an arbitrary tool's
detached subprocess or remote job stopped. Such detachments are outside this
backend contract; the backend must implement and prove their full lifecycle.

An exited observation and its single completion admission are committed in
one coordinator transition. Completion targets the original task directly,
not current Thread subscribers. The public Trigger is a bounded factual
envelope; full receipts/output are not copied into that envelope. Private
output reaches the Worker as a new user-role event with explicit origin,
not a fabricated second tool response. Output is untrusted tool data.

Failed/stopped/unknown operations impose a task review barrier. Existing Worker
execution is not rolled back or automatically killed, but new launches are
fenced and later input deliveries are withheld. PiBackgroundWorker also
fences every subsequent native tool permission check in an already active
turn, including query/stop tools, while review is pending. Trusted host
inspection and StopBackground remain available. Already-entered bodies must
use their native pre-effect permission checks; no rollback is inferred. Completion facts still appear
in the operation record and the blocked delivery's public report. Only after
actual exit can the trusted host reconcile the operation; a note cannot prove
an unknown operation exited. Inputs already admitted before reconciliation
stay withheld. Send fresh instructions after review. Other independent tasks
can continue within remaining quota.

Receipt committed=true preserves a trusted local commit fact. False remains
unknown, never a no-effect guarantee. There is no automatic rollback, safe
retry inference, or model-text override of execution facts.

## Recovery, lifetime and limitations

The coordinator logs reservation, acknowledgment/observations, stop, terminal
facts and host review. Older records need no migration. Old binaries reject
unknown background transition states rather than silently treating them as
normal task records. Reopening a background journal through ordinary OpenMain
without explicit backend configuration fails closed.

`NewLocal` retains operations only in memory. Reopening the coordinator with
the same still-live backend instance can re-query a running operation. A whole
process/registry loss produces unknown, holds quota and needs host backend
verification. Stop on a never-seen ID is only an unknown tombstone, not proof
that an earlier process exited. Only a later attempted Start in that instance
can confirm its prestart fence; coordinator recovery never performs that Start.
This implementation is not a durable remote job service or process supervisor.

`SuperAgent.Close` stops coordinator observation and Workers, not independent
background operations. The host owns the backend lifetime and must explicitly
stop/cancel it when wanted. `WaitIdle` waits for currently admitted Worker/main
inputs, not for all background operations. Observe operation state separately.
A non-cooperative backend callback may delay its caller; no timeout is faked
as proof of termination. Polling defaults to one second and is host-configurable.

Limits are 1–256 coordinator slots, 1–64 registered tools, 64 KiB launch
argument JSON, 64 KiB encoded private output and 256 captured receipts per local
operation. Registry/history retention is unbounded and needs later compaction.
Host callback code, authorization race-free enforcement, isolation, filesystem,
network, descendants and resource policy remain the backend's responsibility.
No shell, binary, account, credentials, remote environment or tool is enabled
implicitly. Batch-main integration and durable remote/backend reconciliation
adapters remain later slices.

## Verification

Tests use channels and scripted models, never real providers. See the runnable
external-package `ExampleOpenMainBackground`. Final Linux Go 1.26.10 checks are
recorded after implementation and independent review below.

- `go test -mod=readonly -count=1 ./...`
- `go vet -mod=readonly ./...`
- `go test -mod=readonly -race -count=1 ./...`
- Coordinator background/native Pi/example race tests, 20 repetitions.
- Local backend race tests, 20 repetitions.
- Separate `examples` module readonly tests and vet.
- Independent read-only review and targeted race retests. Review fixes cover
  bounded prevalidated completion, stop-before-ack status, stale capability
  fencing, detached report facts, unknown receipt preservation, replay
  admission validation, and backend parent-before-child receipt ordering.
- Gofmt and whitespace checks. No live model/provider requests, manual CI,
  merge, deployment, desktop execution or extra paid execution.

PowerShell is unavailable in this remote container; validation uses the
available POSIX shell without adding repository shell dependencies.
