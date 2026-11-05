# SuperAgent

For the persistent main-session Go host API, its upgrade boundary and durable
reply contract, see [M1a](../docs/superagent-m1a.md) and `ExampleOpenMain`.
For durable structured steering/cancellation and exact execution boundaries,
see [M1b](../docs/superagent-m1b.md) and `ExampleSuperAgent_MainControls`.
The existing `Open` examples below retain the stateless Router contract.

SuperAgent is an opt-in coordinator in the pi-go module. An incoming conversation
can start a task or join one started elsewhere. Each task has its own Pi agent
snapshot. A later event from a subscribed thread goes directly to that task.
Programs that use a single agent can continue to import `github.com/Icatme/pi-go/agent`.

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

The demo starts two tasks. A simulated build event joins the release task on
its second turn while the support task keeps its own history. One repeated
delivery is acknowledged as a duplicate. The model in this demo is a
deterministic fixture running through the same Pi Runner as a configured model.

To supply events and keep their state across runs:

```powershell
New-Item -ItemType Directory -Force .data | Out-Null
Get-Content superagent/testdata/events.jsonl | .\bin\pi-superagent.exe run -state .data/superagent.jsonl
```

Run the same command twice. The second run acknowledges duplicate inputs and
replays the stored reports. With stdin open, the process can accept further
events and emit completed reports concurrently. Consumers can deduplicate
replayed reports by `(source, event_id)`. An `ACCEPTED` line acknowledges
durable admission. The eventual `REPORT.status` gives the execution outcome.
At EOF, `IDLE.tasks` shows the current task states. Replayed reports retain
the facts and review flag recorded when each event finished.

The CLI accepts one UTF-8 JSON object per line:

```json
{"id":"27","source":"im","thread":"build-incident","kind":"request","text":"Investigate the failing build."}
```

`source` and `thread` identify the conversation. `id` is unique per source.
Reusing an ID with different contents raises an error. The host classifies
events as `request`, `signal`, or `noise`. Noise receives an ignored outcome
before any model call. The offline fixture recognizes an explicit
`@task-1`-style reference at the start of a signal on a new thread. A real
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
  model messages, tool calls or results.

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

This implementation covers the Router/Worker, thread subscription, asynchronous
ingress, and local persistence parts of the Super-agent design. One process
owns the state file. Capacity and restart time grow with journal history.
Production IM/GitHub adapters, remote execution devices, many-to-many
subscriptions, log compaction and an independent progress watchdog are future
extensions.
