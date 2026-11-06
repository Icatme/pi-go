# M9a: offline experiment and acceptance baseline

This development slice is stacked on draft #172, commit
`89d8c23c1fde5df588f84a23f5b6f5ce52ce6e18`, tree
`0a95381380954db0ec9509802ce3cba8457dcaf2`. It changes no production scheduler,
provider, log format, dependency, or default execution policy.

## What this establishes

There are two deliberately separate kinds of evidence:

1. `internal/schedulereval` and `cmd/pi-superagent-eval` are a deterministic
   **discrete-event structural simulation**, not execution of SuperAgent or Pi.
   Their service times, scripted intent and host associations are inputs.
2. `superagent/evaluation_backlog_test.go` exercises the **actual public runtime
   entrypoints and journal**, with gated Router/Worker callbacks. It measures
   correctness and callback counts, not elapsed inference or tool performance.

No pre-M1 scheduling performance capture was found in the inspected base.
Existing benchmark files concern checkpoint approvals, tool directories and MCP
catalogs, not this experiment. The legacy `Open` test below proves compatibility
at the current base, not historical performance. The output records this missing
baseline instead of reconstructing or inventing measurements.

This is not final M9 acceptance. In particular, no four-way Pi/model comparison,
semantic routing accuracy, production latency, model token cost or speedup has
been established.

## Reproduce without a service

Use the repository Go toolchain and cached dependencies. These commands do not
select a provider, look up credentials, make model calls or start external tools.
All experiment time is virtual. Example PowerShell commands:

```powershell
$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:GOTOOLCHAIN = 'local'
$env:PIGO_LIVE_TEST = '0'
$env:PI_GO_AGENT_LIVE_TEST = '0'
$env:PI_GO_AGENT_EXAMPLE_LIVE = '0'
go run -mod=readonly ./cmd/pi-superagent-eval > result.json
go run -mod=readonly ./cmd/pi-superagent-eval -summary > summary.json
go test -mod=readonly -count=1 ./internal/schedulereval
go test -mod=readonly -count=1 ./superagent -run '^TestEvaluation'
```

`result.json` contains the workload, oracle, every delivery, task state, batch,
queue wait, terminal/reply timestamp and completion curve for every repetition.
The checked-in [compact baseline](../internal/schedulereval/testdata/baseline.json)
contains per-repetition metrics and a SHA-256 of `json.Marshal(fullSuite)`; that
hash is of canonical compact Go JSON, not the pretty-printed output bytes.
`TestCheckedInBaseline` checks it against a fresh full run. Changing the fixture
or method requires reviewing and regenerating the baseline, not just accepting
new performance numbers. Full records are regenerated rather than committing
multi-megabyte duplicate traces.

## Fixed mixed workload and policy matrix

The versioned `Fixture()` generator produces exactly 300 deliveries, 280 unique
`(source, id)` events, 20 work requests, 20 follow-ups, 20 CI callbacks, 4 cancels,
216 noise events and 20 repeated callbacks. Four chats each contain five topics;
CI callbacks use a different source. The complete fixture and separate expected
action/task oracle are included in the output. The SHA-256 is recorded in the
baseline. There is no random seed, wall clock, network or nondeterministic ID.

The two simulated policies share four tool slots; task service time is fixed at
120, 130 or 140 virtual seconds, with a 2-second cooperative stop for running
work. A queued cancellation does not start a tool. Router service time is
`1000 + 100 * batch_size` virtual milliseconds, at most 8 inputs and 32 KiB of
serialized synthetic input per batch. Fixed Worker service is counted once per
started task; associated follow-ups do not add model turns or extend its service.
This last simplification must not be mistaken for Pi Worker behavior.

| Planned group | Evidence in this slice |
| --- | --- |
| Full scheme | `full_architecture_surrogate`: scripted scheduling with known associations bypassing Router. It is not the complete M1–M8 implementation. |
| Subscription bypass disabled | Same structural simulation, but follow-ups/callbacks use the Router. Exact cancellation remains a host control in both groups. |
| Single Pi + subtask tool, neutral prompt | `missing`; no equivalent single-Pi subtask/prompt harness is implemented. No result rows or synthetic substitute. |
| Single Pi, delegate-first prompt | `missing` for the same reason; scripted intent cannot establish a prompt-policy effect. |

Each implemented group repeats five times, with identical output. These repeats
check reproducibility only; they are not independent samples, confidence
intervals or evidence about stochastic model behavior. Four strategy entries
always remain present, including the two missing groups.

The script acts on event kind/work association, never the oracle. The oracle is
used after execution. Fault-injection tests drop, misroute or change an update
and verify that both event metrics and correct task completion fall. This checks
the scoring machinery, not a model's ability to understand a conversation.

## Metric definitions and observed synthetic result

- Every delivery has exactly one record. Duplicate delivery is separate from a
  new terminal decision; conflicting duplicate payloads are rejected by fixture
  validation and by the actual API tests.
- Correct event: observed action and task match the oracle, with no rejection.
  Missed route: a non-ignore expected action is not correctly applied. Misroute:
  a wrong nonempty task was selected. These counters can overlap.
- Actual delegations and tool starts are distinct. Twenty scripted tasks are
  created, but queued cancels mean only seventeen tools start in this fixture.
- Correct task completion/cancellation requires the expected terminal state and
  **all** associated inputs to be correctly handled. A wrong assignment taints
  both its expected and observed tasks. Raw terminal counts remain separate.
- First effective reply is the main reply for a correct completion or correct
  cancellation, measured from the original request; an admission acknowledgement
  does not count. Per-task `reply_ms` is the raw virtual timestamp; the metric
  excludes incorrect tasks. Its sample count is `effective_replies`.
- Completion p50/p95 covers the sixteen correctly completed, non-canceled tasks,
  from request arrival to tool terminal time, before reply processing. Percentiles
  use nearest rank. Empty samples are null, not zero. The cumulative completion
  curve uses absolute virtual terminal time and counts correct completions only.
- `script_router_calls` counts batches, including synthetic completion/cancel
  replies. `script_worker_calls` counts the fixed task service invocations.
  Real model calls are zero. Input/output tokens are **null/missing**, never
  estimated from bytes or reported as model usage. No monetary cost is asserted.

For each of the five runs, both simulated policies produce 280 correct unique
events, no misroutes/missed routes, 16 correct completions and 4 correct cancels.
The bypass policy uses 39 scripted Router calls versus 75 without the bypass.
Both have the same completion p50/p95 (303,000 / 563,000 ms), and the same first
effective reply p50/p95 (256,300 / 516,400 ms). There is **no observed completion
latency improvement in this synthetic workload**, and these are not real
latencies. The results only make the assumptions and bookkeeping reproducible.

## Router offered-load sweep

A separate single-server queue simulation emits 300 cold unique inputs at fixed
rates of 1, 5, 10, 20 and 40 per virtual minute. Each rate compares maximum batch
sizes 1 and 8, five times each. Both policies immediately serve an idle first
input; no debounce is introduced. Service time is the explicit assumption
`2000 + 200 * batch_size` ms. Recorded data includes actual batch sizes, per-input
queue wait, batch service time, per-item amortized service time, throughput and
errors. Amortized time is not end-to-end latency. Throughput divides 300 by the
span from first arrival to final settlement, including drain time.

At 1–20/minute the fixture produces no queue wait and no batching. At 40/minute,
size 1 has 300 calls and 198,800 ms queue-wait p95; size 8 has 196 calls and
2,000 ms p95. These values follow from the chosen service formula. They are not
measured model acceleration. The load model contains no injected error; actual
failure isolation is tested separately below. No provider inference or token
measurement is implied by the zero modeled errors.

## Actual runtime acceptance gates

New tests use channel barriers, not sleep-based throughput measurements:

- 300 valid `Submit` attempts (256 unique + 44 duplicate), plus a conflicting
  duplicate rejection probe. With the first Router call held, all other inputs
  accumulate. Maximum batch sizes 1 and 8 yield exactly 256 and 33 callbacks.
  Input order, item/byte bounds, input/event/delivery identity, replies, terminal
  reports, group failure and uninterrupted tail processing are verified.
- A separate 64-request case verifies actual delegation, 64 Worker outcomes and
  64 result inputs, including one Worker failure, without claiming deterministic
  interleaving or a fixed number of mixed result batches.
- Legacy `Open` processes 256 independent single-event tasks and no main replies.
  All three cases close/reopen the journal, resubmit duplicates and assert no new
  dispatch or changed durable outcomes.

Existing acceptance tests, run in the full suite, supply the cancellation and
in-flight recovery evidence. In particular:
`TestMainCancelPreservesLateSuccessAndDropsSteering`,
`TestMainControlReplayEveryActionBoundary`,
`TestMainBatchClosedInferencePreservesWholeGroup`,
`TestMainBatchBlockedInferenceDoesNotBlockExactAbort` and
`TestMainReplayEveryCommitBoundary`. These are not new experiment measurements.

## Later development slice

[M9b](superagent-m9b.md) adds actual Pi/SuperAgent entrypoint execution and two
synchronous single-Pi host-tool arms for a smaller serial protocol fixture. It
does not replace this simulation or fill the full scheduling/performance baseline.

## Remaining release gates

The two single-Pi baselines, actual Pi integration of the common experiment,
semantic routing quality, live Router latency/tokens, cross-platform evaluation,
a pre-M1 historical baseline and final full-stack release acceptance remain
missing. Shared-storage takeover, device protocol and channel simulator coverage
are existing scoped tests, not evidence of real service/device deployment.
No user computer, live service, credentials, additional paid execution, manual
CI dispatch, merge or deployment is part of this slice.

## Validation of this slice

Verified on Linux amd64, Go 1.26.10, using cached modules with `GOPROXY=off`:

- `go test -mod=readonly -count=1 ./...`
- `go vet -mod=readonly ./...`
- `go test -mod=readonly -race -count=1 ./...`
- Focused simulation/runtime acceptance tests with race, repeated five times.
- The separate `examples` module: readonly tests and vet.
- Full CLI output generation, summary/baseline equality, gofmt and whitespace.

The first fixture-shape test failed before implementation. Independent read-only
review reproduced and corrected a zero-stop scheduling gap and scoring of tasks
with misrouted/dropped input; their regressions pass. Catalogue-order independence
now checks all task start times. Final review found no remaining blocker.
PowerShell was unavailable on the verification host, so the commands were run
in its Bash shell; no claim of Windows or PowerShell validation is made.
