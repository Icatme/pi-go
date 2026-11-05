# M1a: persistent main-session contract

This slice adds the Go-host loop `Submit → main decision → Worker → main reply`
on the existing scheduler. It is not the complete M1/M2 implementation. In
particular, steering/cancellation decisions, multi-topic chat routing,
subscriptions, automatic history compaction, and external channel adapters
remain follow-on work.

## Host API and migration

- New hosts use `OpenMain(ctx, path, parallel, MainRouter, Worker)`.
  `PiMainRouter(def, maxContextBytes)` builds a read-only, one-turn model adapter
  that resumes the same private main snapshot. The Router model definition and
  context budget are independent of the Worker's configuration.
- `MainRouter` receives a stable main and provider-session identity, one ordered
  `MainInput`, a detached snapshot, and up to 128 public task summaries. It may
  propose `new`, `attach`, `reply`, or `ignore`. Only a user `request` may start a
  task; a Worker result may only produce a reply or be ignored.
- The coordinator derives destinations, input IDs, decision IDs and reply IDs.
  The model cannot choose a different source, thread or trust domain. Each
  journal belongs to one host authorization domain; the host must authenticate
  adapters and use separate journals for separate domains.
- `Replies()` returns detached durable records. In-memory consumers call
  `AcknowledgeReply(ID)` after reading. An external sender must call
  `BeginReplyDelivery(ID)` **before** sending and then acknowledge confirmed
  delivery. A started send remains `needs_review` until confirmed, including
  after restart. Do not automatically resend an uncertain send. No external
  transport is added here. This preliminary send marker does not yet provide
  a reset operation after a host proves non-delivery. Such a reply must remain
  unresolved; do not falsely acknowledge it to clear the state. The explicit
  reconciliation and retry contract belongs with the external sender slice.
- `MainSnapshot()` is a private host API. `MainFailures()` identifies consumed
  inputs for which inference or validation failed. Raw callback diagnostics
  remain process-local through `MainFailureCause`, not public replies. A main
  failure while consuming a report never replaces the Worker's `FailureCause`.
- `WaitIdle` includes pending main results and decisions, but never waits for
  external reply acknowledgement. `ReportsSince` continues to count terminal
  **input-event reports**, not replies. Do not use its cursor as an outbox cursor.
- Existing `Open`, `Router`, `PiRouter`, `ExampleOpen`, and `cmd/pi-superagent`
  continue to use their stateless contract and existing reports. They share the
  Worker scheduler; there is no second Worker scheduler. To opt in, change the
  constructor and consume `Replies` as shown by `ExampleOpenMain`.
- The first `OpenMain` writes a durable activation boundary. Already completed
  legacy reports do not generate new replies. Previously unassigned inputs are
  adopted, and future admitted inputs use the main contract. Existing task IDs,
  snapshots, provider-session IDs, review barriers and deduplication keys remain.
  After activation, `Open` refuses that journal without dispatching work or
  appending recovery transitions. Storage opening can still repair a torn
  trailing fragment before this check. Older
  binaries do not understand the new records: stop the single writer and keep
  a backup before upgrading; do not roll an upgraded journal back to old code.

## Persistence and failure boundaries

The inbox is a deterministic projection of admission and task terminal
records, including deliveries blocked by a task's review barrier. Input,
decision and reply identities include the persisted random main-session ID,
so different journals sharing an external account cannot collide. A Worker
terminal record creates its main result during the same
reduction, so a process exit cannot fall between reporting the result and
queuing its wake. Router failures, ignored input reports, replies, and reply
acknowledgements do not recursively create result events.

One `main_decided` record contains the consumed input identity, validated main
snapshot, decision, and assignment or outbox identity. Validation happens
before append and again during replay. Workers run only after that record is
synced. Main inference runs outside the scheduler lock and does not use a
Worker slot. It has no durable start because it is required to be read-only;
an uncommitted input can be inferred again after restart using the same IDs.
Host-supplied callbacks/models remain trusted Go code, not a security sandbox.

Worker dispatch still syncs `running` before execution. A restart without a
terminal record produces `interrupted` and `needs_review`, never an automatic
retry of an uncertain effect. The main result and its eventual reply carry the
original structured status, receipts and review requirement alongside any
generated text. Render those facts; generated prose is not an authoritative
execution receipt.

The Pi adapter inherits model configuration only. It does not inherit tools,
execution hooks, dynamic tool resolvers or request hooks. Persisted model
selection excludes provider credentials/configuration and raw error strings.
It enforces a serialized-input byte budget before making a model request;
exceeding that budget is an explicit main failure, not silent history loss.
This bound is not a model-specific token estimator or a compaction policy.

M1a keeps the legacy single-task thread binding. Inputs already admitted to the
main inbox stay in that ordered inbox even if an earlier decision later binds
their thread. New known-thread inputs still follow the current Worker fast
path. Separating independent requests in an already bound chat is M2.

## Verification

The first regression is the external-package
`TestMainSessionWorkerReplySurvivesReopen`, with no CLI or network/model service.
Additional tests cover every durable commit prefix, uncommitted inference,
invalid/panicking main callbacks, admission order, a blocked Router alongside
known-task control, private snapshot isolation, uncertain delivery, legacy
upgrade, Pi authority filtering and input budgets. Prefix replay tests model
process loss at durable boundaries; they do not replace filesystem torn-write
or actual process-kill tests already present for the journal/Worker runtime.

Run on an authorized machine with the repository's required Go toolchain:

```sh
go test -mod=readonly -count=1 ./superagent
go test -mod=readonly -race -count=1 ./superagent ./agent/session
go test -mod=readonly ./...
go vet -mod=readonly ./...
```

Verified on 2026-11-05 in a Linux amd64 cloud workspace with official Go 1.26.10
(archive SHA-256 checked against go.dev), based on commit
`5d3402bc160c068a25a7ccfbb3c72f383e9259a0`:

- Baseline `go test -mod=readonly -count=1 ./superagent` passed.
- Adding only `TestMainSessionWorkerReplySurvivesReopen` to the baseline failed
  to build because the persistent-main API did not exist. Applying this slice
  made that same test pass.
- `gofmt` and `git diff --check` passed.
- `go test -mod=readonly -count=1 ./superagent` passed.
- `go test -mod=readonly -race -count=1 ./superagent ./agent/session` passed.
- `go test -mod=readonly -count=1 ./...` and `go vet -mod=readonly ./...` passed.
- In `examples`, `go test -mod=readonly -count=1 ./...` and
  `go vet -mod=readonly ./...` passed.
- The new main/PiMain/blocked-main tests and `ExampleOpenMain` passed an
  independent `-race -count=20` run.

The first full run caught a test-only assumption that the initial snapshot
message was user text; the Runner first persists a system-transcript record.
The isolation test now locates a user text message explicitly, and the final
full suite passed. All models were offline fixtures. No manual CI dispatch,
live provider, external message delivery, or Windows/macOS validation was used.
