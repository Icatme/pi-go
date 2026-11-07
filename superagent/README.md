# SuperAgent

For the persistent main-session Go host API, its upgrade boundary and durable
reply contract, see [M1a](../docs/superagent-m1a.md) and `ExampleOpenMain`.
For durable structured steering/cancellation and exact execution boundaries,
see [M1b](../docs/superagent-m1b.md) and `ExampleSuperAgent_MainControls`.
For exact reply delivery attempts, verification, safe retries and echo filtering,
see [M1c](../docs/superagent-m1c.md) and `ExampleSuperAgent_BeginReplyDelivery`.
For same-chat independent work, explicit task updates, and persisted correction
evidence, see [M2a](../docs/superagent-m2a.md) and `ExampleSuperAgent_SubmitTask`.
For optional same-host SQLite leases, fencing and JSONL import, see
[M6a](../docs/superagent-m6a.md) and `ExampleOpenMainJournal`.
The opt-in [local helper backend](../agent/background/helper/README.md) adds
durable background identities and host-registered Linux helper processes.
Recovery observes the original operation without relaunch; unknown execution
retains review and quota. It is not a sandbox or real-device acceptance.
The bundled leased journal uses [SQLite on one host](sharedsqlite/README.md)
with a local filesystem. Hosts that need PostgreSQL or another shared backend
implement the public `Journal` and `LeasedJournal` contracts and pass an already
claimed journal to `OpenMainJournal` with `MainJournalOptions`. The host owns its driver,
schema, connection lifecycle, lease renewal and backend validation.
For opt-in read-only Watchdog inspection, independent clock/session and durable
recheck evidence, see [M8a](../docs/superagent-m8a.md) and
`ExampleSuperAgent_CheckWatchdog`. For a separately host-authorized, durable
exact-operation Inspect, see [M8b](../docs/superagent-m8b.md) and
`ExampleSuperAgent_RefreshWatchdog`. No automatic recovery actions are exposed.
For bounded, one-shot follow-up evidence on completed interventions, see
[M8c](../docs/superagent-m8c.md) and
`ExampleSuperAgent_WatchdogFollowupsSince`. The existing patrol observes committed
coordinator facts without another backend call or external notification. Hosts
own the consumption cursor and any separately authorized delivery. Checks retain
the old reader contract until the first follow-up evidence is committed. After
that upgrade, including subsequent empty checks, older readers fail closed.
The existing `Open` examples below retain the stateless Router contract.

SuperAgent is an opt-in coordinator in the pi-go module. An incoming conversation
can start a task or join one started elsewhere. Each task has its own Pi agent
snapshot. With `OpenMain`, normal chat messages always reach main; only explicit
`SubmitTask` updates go directly to a known task. With legacy `Open`, a later
event from a subscribed thread goes directly to that task.
Programs that use a single agent can continue to import `github.com/Icatme/pi-go/agent`.

`PiTaskAssessor(def, maxContextBytes)` optionally classifies one input as `new`,
`attach`, `clarify` or `status`, together with `light`, `conversation` or `task`
workload. The host supplies bounded `TaskCandidate` values and owns `CanAttach`,
context selection, authorization, persistence and execution. `TaskAssessmentInput.Validate`
can check the envelope before constructing a model. Assessment uses one isolated
`decide_task` inference with `TaskAssessmentInstructions`; it inherits only the
configured model and static reasoning/transport settings, never business prompts,
tools, history, resolvers or hooks. The byte budget includes policy, input and tool
declaration; excess context fails without truncation. `ErrInvalidTaskAssessment`
identifies input/protocol errors, while provider and cancellation errors remain
distinct. It neither modifies a journal nor replaces the existing MainRouter,
task attachment, history or worker-report APIs. Give each assessment its own
`SessionID` when provider request identities also serve business invocations.

The [development plan](../docs/superagent-development-plan.md) tracks the
resident Router, Worker handoffs, asynchronous tools, and recovery milestones.

## Embed in a Go service

Import `github.com/Icatme/pi-go/superagent` alongside `github.com/Icatme/pi-go/agent`.
Create one coordinator at service startup and close it at shutdown. Pass the
host's model, authorized tools and hooks through its usual `AgentDefinition`.

```go
func openTasks(serviceCtx context.Context, journal string, def agent.AgentDefinition) (*superagent.SuperAgent, error) {
	router, err := superagent.PiRouter(def)
	if err != nil {
		return nil, err
	}
	worker, err := superagent.PiWorker(def)
	if err != nil {
		return nil, err
	}
	return superagent.Open(serviceCtx, journal, 4, router, worker)
}
```

Send work with `s.Submit(superagent.Trigger{ID: id, Source: source, Thread: thread, Kind: "request", Text: text})`.
The returned boolean distinguishes a new admission from a duplicate. Use
`s.ReportsSince(cursor)` for completed outcomes, or `s.WaitIdle(ctx)` to wait
for all admitted events. Call `s.Close()` in the service shutdown path.
The [runnable Go example](example_test.go) submits an event and reads its
report using the offline model.

The public `Journal` interface consists of `Header() session.Header`,
`Log() []session.LogItem`,
`AppendEntry(string, session.NewEntry) (session.Entry, error)` and `Close() error`.
It stores a coherent, ordered main-lane log using the public `agent/session` types.
`LeasedJournal` embeds `Journal` and adds `Context() context.Context`.
Each append on a leased journal must atomically verify the current
unexpired ownership epoch and expected log head together with its durable write.
`Header`, `Log` and `AppendEntry` return detached values. Losing ownership or an
uncertain commit must cancel `Context` and stop further writes. `Close` releases
only that owner's epoch. Expired epochs cannot renew. The lease covers the whole authorization
domain, with parallel Workers inside its owner. See the authoritative
[Journal and LeasedJournal contracts](journal.go).

Set exactly one of `MainJournalOptions.Router` and `BatchRouter`, supply `Worker`
and `Parallel`, and explicitly configure `Background` when required. After
argument validation, `OpenMainJournal` owns the journal, including replay failure.
Its `Close` waits for callbacks before closing the journal. Host implementations
must retain the existing interrupted-work and uncertain-delivery review barriers.

## Consuming reports without losing fanout outcomes

`Report.DeliveryID` (`delivery_id` in JSON) identifies one admitted delivery,
including ignored or router-failed outcomes that have no task. Each recipient
of a `SubmitThread` fanout has a distinct delivery ID, even though its reports
share the same `(source, event_id)`. Ingress deduplication still uses that
source/event pair; it is **not** a report-consumer key.

Delivery IDs are opaque, stable across reopening the same journal, and local
to that journal. Use `(host-owned stable journal namespace, delivery_id)` as
the sink's idempotency key. Do not parse an ID, rely on its format, assume it
is globally unique, or carry the same namespace onto an unrelated replacement
journal. Existing journals acquire the same IDs on replay without a rewrite.

`ReportsSince(cursor)` still takes a **count of reports**, not a delivery ID or
an admission offset. Advance the count by the number of successfully consumed
reports. Fanout may contribute several reports per ingress event; concurrent
tasks may finish in a different order than admission. Keep the count scoped to
the same journal. Starting again at zero is safe only with durable per-delivery
deduplication at the sink. Commit the effect and dedup key atomically, or use a
destination that enforces the same idempotency key. Persisting a cursor before
the effect can lose work; saving the effect before its dedup key can repeat it.
An in-memory map alone does not provide crash-safe exactly-once effects.

The public Go example `ExampleSuperAgent_ReportsSince` shows a count cursor,
two recipients for one event, and replay through an idempotent in-memory sink.
`Job.DeliveryID` and `Progress.DeliveryID` correlate worker diagnostics with
the report. They do not replace the existing `(task_id, source, event_id)`
`RunTarget` control boundary. A main `Reply.ID` remains the separate outbox
send/acknowledgement identity; a delivery ID is not a reply-send receipt.

To control one invocation, capture its identity from a running entry in
`s.Status().Tasks` when the host accepts the operator's command:

```go
target := superagent.RunTarget{TaskID: task.ID, Source: task.ActiveSource, EventID: task.ActiveEventID}
err := s.Abort(target)
```

`s.Steer(target, text)` returns a receipt whose `Wait(ctx)` reports delivery.
`s.Subscribe(ctx, task.ID)` returns a bounded progress channel. An unread
channel can miss older updates while other goroutines continue to call
`Abort`. A worker that has returned enters `finalizing` until its outcome
is committed. For diagnostics, use `s.TaskSnapshot(task.ID)` and the
process-local `s.FailureCause(source, eventID)`.

## Optional CLI demo

From the repository root on Windows:

```powershell
go build -o bin/pi-superagent.exe ./cmd/pi-superagent
.\bin\pi-superagent.exe demo
```

The demo starts two independent tasks in one chat. A simulated build event joins the release task on
its second turn while the support task keeps its own history. One repeated
delivery is acknowledged as a duplicate. The model in this demo is a
deterministic fixture running through the same Pi Runner as a configured model.

To supply events and keep their state across runs:

```powershell
New-Item -ItemType Directory -Force .data | Out-Null
Get-Content superagent/testdata/events.jsonl | .\bin\pi-superagent.exe run -state .data/superagent.jsonl
```

Run the same command twice. The second run acknowledges duplicate inputs and
replays the stored reports and main-session replies. With stdin open, the process can accept further
events and emit completed reports and replies concurrently. Consumers must deduplicate
replayed reports by `(journal namespace, delivery_id)`, as described above.
Both `run` and `demo` use `OpenMain` with `PiMainRouter`; a shared chat never
automatically attaches a new request to an old task. An `ACCEPTED` line acknowledges
durable admission. The eventual `REPORT.status` gives the execution outcome.
At EOF, `IDLE.tasks` shows the current task states. Replayed reports retain
the facts and review flag recorded when each event finished.

`REPLY` is a main-session outbox observation with a stable `id`, host-owned
`source`/`thread`, original `report` and any structured `reports` citations.
Deduplicate replies by `(journal namespace, id)`, not delivery ID: several
replies can concern the same input delivery. All replies replay on restart,
including their current `pending`, `needs_review` or `acknowledged` status.
Successful stdout output neither consumes the reply nor proves platform delivery.
After directly consuming one reply, send:

```json
{"op":"ack_reply","reply_id":"<copy the actual REPLY id>","request_id":"read-1"}
```

`REPLY_ACKNOWLEDGED` confirms durable direct consumption only. It is idempotent
and cannot resolve an external send attempt. A transport adapter must instead
use the Go `BeginReplyDelivery` / `ResolveReplyDelivery` / `ObserveReply` protocol
with verified evidence. Never blindly forward replayed output to a platform.
`MAIN_ERROR` exposes a persisted, safe main-router failure by `input_id`, not
private provider errors. `-main-context-bytes` defaults to 1 MiB and fails
explicitly instead of truncating main history. See the [CLI migration contract](../docs/superagent-cli-main.md).

Stop old writers before opening an old journal with this CLI. Existing reports,
tasks and Worker/provider history are preserved, without treating completed
legacy reports as fresh main inputs. This is a one-way `OpenMain` upgrade;
legacy `Open` cannot subsequently write that journal.

The CLI accepts one UTF-8 JSON object per line:

```json
{"id":"27","source":"im","thread":"build-incident","kind":"request","text":"Investigate the failing build."}
```

`source` and `thread` identify the conversation. `id` is unique per source.
Reusing an ID with different contents raises an error. The host classifies
events as `request`, `signal`, or `noise`. Noise receives an ignored outcome
before any model call. The offline fixture recognizes an explicit
`@task-1`-style reference at the start of a signal, including in the same chat. A real
router uses the event text and task summaries to make that decision.

Supply both `-provider` and `-model` to use a registered pi-go provider with
its normal credentials. The CLI worker has no execution tools. An embedding
application can pass its own model and authorized tool definitions to `PiWorker`.

## Controlling an active task

In an interactive `pi-superagent run`, send control frames on the same local
stdin as your triggers. Start with `status` and copy the task's `id`,
`active_source` and `active_event_id`. These identify the particular invocation:
an old control is rejected if another event has since started on that task.
For example, while event `27` from `im` is running:

```jsonl
{"op":"status"}
{"op":"subscribe","task_id":"task-1","request_id":"watch"}
{"op":"steer","task_id":"task-1","source":"im","event_id":"27","text":"Use the staging environment.","request_id":"change"}
{"op":"abort","task_id":"task-1","source":"im","event_id":"27","request_id":"stop"}
{"op":"unsubscribe","task_id":"task-1"}
```

The dispatcher applies controls while stdout is blocked. An abort can reach
the worker before the client reads its acknowledgement. The CLI writes frames
in emission order. When its memory queue fills, it buffers additional frames
in temporary files in the OS temp directory and removes them after delivery
or an output error. The Go API calls the coordinator directly.

- **Steer.** `STEER_QUEUED` acknowledges admission to the running worker.
  `STEER_DELIVERED` means Pi polled the instruction at a steering boundary.
  If the run ends or is aborted first, the CLI emits `STEER_NOT_DELIVERED`.
  The model can act on steering in a subsequent request; it cannot change a
  tool call already in progress. Each run can queue 16 instructions of at most
  4 KiB each. Pending controls are process-local and are never resent after
  restart. A CLI `request_id` correlates replies; sending the same steering
  frame twice queues two instructions. Use a regular trigger for a durable,
  deduplicated follow-up.
- **Abort.** `ABORT_REQUESTED` acknowledges cancellation of that invocation.
  The worker retains its slot until its executors exit. Its terminal report
  records `interrupted` and any observed tool receipts. The task then requires
  reconciliation before further work. Other tasks can keep running.
- **Subscribe.** `PROGRESS` frames carry task and event identity, a phase and
  a per-task sequence number. Subscriptions span successive events on a task.
  PiWorker publishes turn/tool start and end phases. The coordinator publishes
  control and lifecycle phases. Each subscriber buffers the latest 16 updates;
  a sequence gap identifies missed updates. Consult `REPORT` for the durable
  outcome. A subscription exposes phase names rather than the contents of
  model messages, tool calls or results. These process-local observations do not
  change durable `SetThreadSubscription` routing authorization. This CLI does
  not expose Thread-subscription or `SubmitThread` commands.

Cancel the subscription context to unsubscribe. Custom workers poll `Job.TakeSteering` at their own
safe boundaries, observe their job context for cancellation, and may call
`Job.NotifyProgress` to report the four supported worker phases. Only trusted,
authorized host code should construct controls from external channels.

## Execution and recovery

- `Open(ctx, path, parallel, router, worker)` opens one journal with an exclusive
  writer. Use a separate journal per trusted identity or team. Source labels in
  stdin are trusted adapter metadata, not authenticated identities.
- A single Router assigns new threads. It sees the incoming event and short
  summaries of up to 128 recently created tasks. It can create, attach, or ignore.
  Each thread subscribes to one task; multiple threads can converge on that task.
- Work on a single task runs serially. Independent tasks run up to the configured
  concurrency limit. Only the coordinator publishes reports. `Status` and
  `ReportsSince` expose outcomes. `TaskSnapshot` gives a trusted host a copy of
  the latest validated snapshot for detailed inspection. If a Pi run fails
  or is aborted after accepting messages, this includes the partial conversation,
  delivered steering and any recorded tool results.
- The coordinator reuses `agent/session.JSONLStorage` to record admission,
  dispatch and completion. The terminal record stores the report, available
  tool receipts and any validated snapshot together. If a worker started and
  its outcome is missing on restart, its event becomes `interrupted` and the
  task enters `needs_review`. Other tasks can continue.
- The local journal contains event bodies, model conversations and possibly tool
  outputs. Keep it private. Hosts wiring external channels must authenticate
  incoming events and authorize which reports can leave that trust boundary.
- A newly created task gets a provider session ID independent of its local
  task ID. Pi model requests and runtime resolvers see that provider session
  ID. Use `Job.TaskID` for controls and `TaskSnapshot` lookups. Reopening the
  journal reuses the provider session ID. Tasks restored from journals written
  before this field existed continue to use their original task ID on the
  provider connection. A standalone `PiWorker` invocation can supply
  `Job.ProviderSessionID`, which defaults to `Job.TaskID`. Saved snapshots
  retain the selected provider and model. The host supplies current connection
  settings and credentials.

## Reading the result of a tool run

`PiWorker` uses the receipt-aware Pi runner for the host's configured tools.
The worker compares the model's requested calls with completed top-level calls.
Child calls contribute their own error and receipt information. After the
model stops, the worker waits for admitted tool bodies to exit before
finalizing its report. A tool that ignores cancellation can delay shutdown.
The router receives no execution tools.

| Report or receipt | What the host can conclude |
| --- | --- |
| `completed` | The model run reached a supported successful end, including a tool that explicitly terminates the run. Examine receipts for external work. |
| `truncated` | The model reached its length limit. Inspect the partial transcript before issuing a follow-up. |
| `completed_with_tool_errors` | The model ended after a tool reported an error. Its final text cannot certify that the requested action succeeded. |
| `receipts[].committed: true` | That executor explicitly reported a local effect. A false flag leaves the effect unknown. |
| `receipts[].completed: true` | The executor body exited. It may have returned an error after publishing its change. |

For example, a file can be created successfully and an after-tool hook can
subsequently withhold the result. The report carries both the committed file
receipt and the tool error status. Tool receipts are scoped to their trigger;
the numeric token links a nested executor to its parent within the same run.
Callbacks that need trusted failure details can be diagnosed in the current
process through `FailureCause(source, eventID)`.

## Releasing a task after an uncertain outcome

Read the task's `review_source` and `review_event_id` and inspect the actual
destination (for example, the file or remote operation). While the task is held,
ordinary follow-ups for it receive a `blocked` outcome. A source event cannot
clear the hold. A trusted embedding host can use `SuperAgent.Reconcile` while
it owns the journal. With the process stopped, use the local command:

```powershell
.\bin\pi-superagent.exe reconcile -state .data/superagent.jsonl -task task-1 -source im -event 27 -note "Inspected the destination and confirmed the file is present."
```

Confirmation records the operator's note for this particular incident. Submit
a fresh instruction after confirmation. Old deliveries stay deduplicated and
blocked commands are never replayed by reconciliation. A crash between an
external effect and the terminal journal write can leave no surviving receipt.

The coordinator implements persistent main routing, many-to-many subscriptions,
background tools, optional GitHub HTTP/device protocols and a read-only Watchdog
with separately host-authorized operation inspection. JSONL has one writer;
shared SQLite has one leased owner on a local host. Capacity and restart time
still grow with journal history. Journal compaction, automatic device scheduling,
general Watchdog repair and real platform/device acceptance remain separate work.

## Bounded main batches and admission gates

`OpenMainBatch` and `PiMainBatchRouter` process accumulated inbox inputs in one
read-only inference, with explicit input IDs, item/JSON-byte bounds and atomic
all-or-nothing decision groups. Idle input starts immediately. Failed groups
produce durable per-input failures, with no automatic retry or partial actions.

Hosts can call `Admit` with an authenticated envelope and versioned exact
source/principal/group rules. Noise and unassociated signals are audited without
model calls; task and Thread associations retain their existing permissions and
frozen delivery semantics. Privileged Submit APIs remain outside this opt-in gate.

See [M3a contract and migration](../docs/superagent-m3a.md) and the executable
`ExampleOpenMainBatch`. Large-history performance and live model throughput remain
separate work.

## Public history references and recall

Main callbacks receive `MainJob.History` (or `MainBatchJob.History`) and stable
`MainInput.HistoryRefs`. Follow trigger, Thread, task and public-report links,
including tasks outside the 128 recent candidates. Each read and continuation
checks the durable journal domain and bounded JSON/page budget. The view is
immutable for the callback and expires when it returns. References survive
replay; authenticated cursors can continue in later callbacks of the same
coordinator while the exact public record is unchanged. They do not survive
coordinator restart.

`PiMainHistoryRouter` and `PiMainBatchHistoryRouter` optionally expose only the
native `history_read` tool with isolated execution authority, bounded turns,
marked input excerpts and a context check before every model request. Existing
Pi main routers remain tool-free. Recall never expands attachment/control offers.
Private Worker transcripts and raw tool outputs remain inaccessible.

See [M3b](../docs/superagent-m3b.md) and the [M3c continuation contract](../docs/superagent-m3c.md).
Cross-restart continuation, explicit tool-result publication and production-scale
history performance remain later M3 work. Model-context compaction is separate
from public history or journal compaction; see [M3d](../docs/superagent-m3d.md).

## Optional main-context compaction

All four Pi main-router constructors accept one optional `MainContextCompactor`.
Without it, context overflow still fails explicitly. On an oversized upcoming
request, the host callback receives detached prior messages and returns
`MainContextCompaction{Summary: summary, KeepFrom: index}`. The replacement must
retain a complete recent turn and cannot split native tool calls from results.
It runs at most once before the current input, never between history tool turns.

The summary is marked as untrusted historical context. Current input, task/run
offers, model configuration, tools and session identities cannot be rewritten.
The full encoded request remains bounded after compaction. Errors, cancellation,
invalid cuts and oversized replacements fail rather than dropping more history.
Only a valid main decision persists the compacted snapshot atomically with its
actions. Original journal events, reports, reply attempts and history references
remain intact, including after restart.

The host supplies and authorizes its summarizer; the library makes no extra model
or service call. This does not verify summary accuracy or compact the journal.
See [M3d usage and compatibility](../docs/superagent-m3d.md).

## Controlled background tools (M4a–M4b)

`OpenMainBackground` explicitly registers bounded, host-authorized backends.
`Job.Background` supplies per-Worker launch/query/stop capabilities;
`PiBackgroundWorker` exposes their native model tools without changing ordinary
synchronous tools. Operations persist their original task/delivery/call, grant
and quota before dispatch, and release capacity only after verified real exit.
The same task can process new input while an operation runs. Completion arrives
once as a new task event, preserving native call/result ordering.

For a batch main router, use the existing `OpenMainJournal` with both
`MainJournalOptions.BatchRouter` (and `BatchOptions`) and `Background`.
[M4b composition acceptance](../docs/superagent-m4b.md) covers mixed input
ordering, native Pi completion, authority, stop/unknown and lease takeover.
The JSONL constructors and their defaults are unchanged.

`agent/background.NewLocal` is an opt-in in-process backend, not a shell,
sandbox or durable process service. Lost registry state remains unknown and
fenced; it is never automatically relaunched. Stop, Worker Abort, actual exit
and committed effects are separate facts. Raw output stays private, while
`Report.Background` preserves output-free authoritative completion facts.

See [M4a contract and limitations](../docs/superagent-m4a.md) and
`ExampleOpenMainBackground`. Close and WaitIdle do not mean independent
background tools have stopped; the host owns their lifetime.

## Optional channel boundary

`superagent/channel` adds host-authenticated IM/GitHub ingress and approved-reply
delivery contracts with offline protocol simulators, durable platform identities,
receipt cursors and source-level rate gates. It contains no live service client.
See [the channel contract](channel/README.md) and [M5a scope](../docs/superagent-m5a.md).

## Optional fixed-device execution (M7a)

`superagent/device` adapts one explicitly selected device to the background
lifecycle. Host authentication and current-authority checks, a private durable
registry, monotonic ownership fences and exact-operation queries prevent blind
relaunch on disconnect. Use the client's full-grant Scope in BackgroundTool;
credentials and raw tool output stay on the device. No listener, pairing,
automatic device selection, fleet quota or production service is enabled.
See [the device contract](device/README.md) and [M7a scope](../docs/superagent-m7a.md).
