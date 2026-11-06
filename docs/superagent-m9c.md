# M9c: concurrent offline runtime load and batch conservation

Based on draft #186 HEAD `c0597812ac5518c06b06da3861b28b0246383952`, tree
`d0aa31b56b32a06a06a3e43097f8b4408fdbaa3f`. This slice adds an internal load
harness, command and tests. It changes no production runtime API, scheduler,
journal, provider transport, dependency, schema, or CI configuration.

## What actually runs

[M9b](superagent-m9b.md) remains a 12-delivery serial four-entrypoint protocol
probe. This separate `m9c-concurrent-runtime-v1` fixture runs **300 deliveries,
280 unique source/event identities and 20 tasks** through two actual runtime
arms, each with four worker slots:

- `superagent_unbatched_subscribed`: `OpenMain`, `PiMainRouter`, `PiWorker`.
- `superagent_batch_subscribed`: `OpenMainBatch`, `PiMainBatchRouter`, `PiWorker`,
  maximum eight inputs and 32 KiB of serialized inputs per batch.

Both arms use actual `Submit` and subscribed `SubmitThread` calls, persistent
JSONL journals, native Pi tool execution/receipts and task snapshots. The
`record` tool appends an observed in-process effect. It does not call a real
provider/platform, execute a command, or make an external change. Its native
receipt is completed, **not committed**; the separate effect observation is
not an external exactly-once guarantee. Replies remain pending outbox entries,
not delivered messages. The model is exclusively an in-process `StreamModel`
script, with no credential lookup or live-provider fallback.

This is a **two-arm batching/conservation experiment**, not the planned
four-strategy M9 benchmark, a comparison with single Pi, prompt-policy efficacy,
semantic routing quality, or real-model performance. No historical pre-M1
baseline is reconstructed. M9a's different 300-message simulated workload and
M9b's four serial arms are not silently substituted for these results.

## Workload, concurrency and clocks

The fixture has three 100-delivery phases:

1. Twenty new tasks in four shared chats, plus 80 noise events.
2. Twenty same-chat corrections, plus 80 noise events.
3. Twenty task-specific GitHub-thread callbacks, their twenty repeats, and
   60 noise events. GitHub event IDs deliberately reuse IM IDs.

Noise is host-filtered by the existing runtime. After phase 1 completes, the
trusted host subscribes each actual task to its task-specific callback thread.
The script resolves corrections only from the offered completed task summaries;
it never reads the scorer's expected assignment. Phase barriers ensure those
summaries exist, so this does not test ambiguous follow-ups arriving before
initial task completion or arbitrary continuous traffic.

Each phase submits one actionable event and holds the first actual router
callback. Sixteen goroutines then admit the other 99 deliveries behind a common
start barrier, before the router is released. There is no per-delivery WaitIdle.
Concurrent repeated events may win admission at either fixture position; the
scorer requires exactly one acceptance per source/event identity.

Native tool bodies wait at a shared gate. All twenty original main inputs in a
phase must have been evaluated before any effect is released. The controller
then observes **four simultaneously gated tool bodies** and releases five
four-body waves. The same tool-service control applies to both arms:

- Tool-gate clock starts at virtual 0 ms and advances exactly 10 virtual ms per
  wave, ending at 150 virtual ms after three phases.
- No sleep pretends to implement those service durations. The recorded start and
  release ticks are **assigned virtual tool-gate time**, not elapsed tool time,
  scheduler latency, completion latency or a throughput measurement.
- Runtime message/journal timestamps and timeout/deadlock guards still use the
  real system clock. This harness does not replace the runtime's clock.
- Concurrent admission/dispatch order, task IDs, batch grouping and the actual
  script-call count may vary. Repeatability means the same normalized effects,
  private task histories and conservation invariants, not byte-identical traces.

The fixture SHA-256, over compact `json.Marshal(LoadFixture())`, is
`bf7fc1874460d4b7df412306041afe67faf47abbdf5c827020ee111278a06857`.
The JSON output contains the fixture, every admission result, raw terminal
report/receipt, pending reply, actual main input group, tool-gate release and
normalized effect/history. It keeps the variable trace rather than publishing
an invented deterministic baseline.

## Validation and observations

All script requests share a 1 MiB serialized ModelRequest bound and a 512-call
per-run ceiling, with no silent history truncation. The Pi adapters independently
apply their full-context bound. Input bytes are not converted into token counts.
The scorer checks:

- Exactly 300 delivery results, one acceptance per 280 unique identities,
  20 duplicates and one terminal report per unique event.
- Exactly 20 distinct task mappings and isolated three-input task histories.
- Sixty worker invocations, native completed `record` receipts, actual effects
  and correctly linked pending replies. Task IDs, source/event/thread,
  delivery IDs, input/decision IDs, call IDs, values and report facts must agree.
- Forty main `user` inputs, twenty informational `task_input` observations and
  sixty `worker_result` inputs, each exactly once and within its phase.
- Actual batch sizes/serialized byte counts, four gated bodies and all sixty
  virtual releases. Reversed script decision order exercises input-ID matching.
- Conflicting duplicate rejection. Completed-journal close/reopen followed by
  replay of all 300 deliveries must not change state or invoke more scripts.

In the initial five-repeat command run, every unbatched run observed 120 main
invocations and 240 total script calls. Batched runs observed 31–33 main
invocations and 151–153 total script calls. Each arm still executes 120 worker
script turns and 60 record tools. This demonstrates fewer **script invocations
for these controlled batches**, not latency improvement, inference quality,
real-model savings, statistical significance, or a guaranteed call count.
`live_model_calls` is zero. `input_tokens`, `output_tokens`, `cost_usd` and
`latency_ms` remain explicit JSON null.

Test-first compilation failed before implementation. Fault tests reject dropped
reports, false admission counts, misassignment, receipt/call-ID corruption,
wrong reply correlation, contaminated histories, invalid batch/input evidence,
clock/concurrency/count corruption, invented costs and missing reopen evidence.
An incomplete response from the actual batch Pi adapter fails the run explicitly.
Whole-run cancellation with all four actual tool bodies gated must join them
without releasing effects. It is not a per-task cancellation/backlog experiment.
Failed runs return errors and are not exported as successful suites; failed-run
receipts and snapshots are not a retained acceptance artifact.

## Reproduce offline

Use the already installed Go toolchain and cached dependencies. PowerShell:

```powershell
$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:GOTOOLCHAIN = 'local'
$env:GOWORK = 'off'
$env:PIGO_LIVE_TEST = '0'
$env:PI_GO_AGENT_LIVE_TEST = '0'
$env:PI_GO_AGENT_EXAMPLE_LIVE = '0'
$env:PIGO_POSTGRES_TEST_DSN = ''
$env:PIGO_POSTGRES_TEST_DISPOSABLE = ''
go run -mod=readonly ./cmd/pi-superagent-runtime-load > runtime-load.json
go test -mod=readonly -count=1 ./internal/runtimeeval
go test -mod=readonly -race -count=3 -timeout=20m ./internal/runtimeeval
go test -mod=readonly -count=1 ./...
go test -mod=readonly -race -count=1 ./...
go vet -mod=readonly ./...
Push-Location examples
go test -mod=readonly -count=1 ./...
go test -mod=readonly -race -count=1 ./...
go vet -mod=readonly ./...
Pop-Location
```

The command defaults to five repeats per arm, accepts `-repeats 1` through `5`,
and removes temporary journals. There is no provider/network option. Repeats
are correctness checks, not independent samples for statistical inference.

The first full-race attempt exceeded the load test's original 90-second guard
while executing its second unbatched repetition, without a race warning. The
whole-load test guard was increased to five minutes; the workload and assertions
were unchanged. Final rerun results are recorded in the draft PR.

Validation runs on the existing remote Linux/amd64 host with cached Go 1.26.10.
PowerShell is unavailable, so Bash equivalents are used; Windows/PowerShell are
not validated here. Full final-tree checks and independent review are recorded
in the draft PR. No user computer, Codex/extra paid execution, real model,
external service, CI dispatch, merge or deployment is involved.

## Still missing for M9

Continuous mixed arrivals, per-task cancellation/fanout under backlog,
interrupted-run crash/recovery, asynchronous single-Pi delegation, stochastic
prompt-policy comparisons, real inference/tool-service timing, first-reply and
completion distributions, throughput, tokens/cost, historical baseline and the
full M1–M8 production stack remain outside this slice. Completed-journal replay
is not crash recovery. The new draft does not close final M9 or release gates.

Follow-up [M9d](superagent-m9d.md) adds a separate bounded, completion-paced
35-submission protocol fixture for exact cancellation/fanout, correction,
post-write failure and one abrupt process-exit boundary. It does not change this
300-delivery fixture into a continuous benchmark or complete the missing M9 gates.
