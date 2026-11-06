# M9b: actual runtime serial protocol comparison

This draft development slice is based on integration draft #184, commit
`62f0a86a833eb5b878b64948edb4c4f21c63f540`, tree
`2b9f018802901715a4df0dd96d8dfb02bcbb2122`. It adds an internal evaluation package
and offline command; no production API, scheduler, journal format, dependency,
provider transport or default behavior changes.

## Evidence and boundary

[M9a](superagent-m9a.md) remains a 300-delivery structural simulation with a
separate callback-based runtime acceptance test. This slice closes a smaller
gap: actual Pi model/tool loops now execute a common fixture through four
entrypoint arms. These are **serial protocol probes**, not the four-way M9
scheduling benchmark or a claim of real-model performance.

Every model is an in-process `agent.StreamModel` script. It never discovers
credentials, resolves a network provider or falls back to live inference.
Neutral and delegate-first prompts are different, but the script deliberately
returns the same decisions. Their equality tests wiring, not prompt quality.
No historical baseline before M1 is available or reconstructed.

The common `m9b-serial-protocol-v1` fixture contains 12 deliveries: 10 unique
source/event pairs and two repeats. Two requests share one chat; each has a
same-chat correction and a GitHub-thread callback. Four noise inputs are
filtered by host admission. One event ID is reused across different sources to
check that deduplication does not accidentally use ID alone. Fixture SHA-256:
`e6e9807995df5ca254f19c709a6ecc6d801289bd68d05220146b72eca2a50bbb`.

| Arm | Actual public entrypoints and host adaptation |
| --- | --- |
| `superagent_subscribed` | `PiMainRouter`, `PiWorker`, `OpenMain`, `Submit`; after the initial task completes the trusted host subscribes its task-specific callback thread, then uses `SubmitThread` for callbacks. Same-chat corrections still use the main Router. |
| `superagent_router_only` | Same Pi definitions, one worker slot, journal and inputs; no thread subscriptions, and all callbacks use `Submit`. |
| `single_pi_neutral` | One persistent `agent.Runner`, a synchronous `subtask` tool and a host-managed single-worker queue that runs the same `PiWorker`. Neutral tool-use prompt. |
| `single_pi_delegate_first` | Identical runner/tool/queue with a delegate-first prompt; the offline script does not simulate a prompt-policy effect. |

The single-Pi queue is explicit because a Runner tool context does not support
starting a nested receipt wrapper. It starts a separate host-owned worker
execution scope rather than removing private context values or changing the
runtime restriction. Cancellation is linked from the caller, and the main run
joins the tool/worker body before returning. The baseline host owns in-memory
deduplication and task snapshots. This is not durable SuperAgent parity, an
asynchronous subtask baseline, or a replacement production orchestrator.

## Shared controls and scoring

- Identical fixture order, one worker slot and deterministic in-memory `record`
  tool. Inputs are submitted only after the previous input settles. Tool work
  is one observed memory append, not a fabricated duration or virtual latency.
- One common 1 MiB serialized `ModelRequest` bound and 128-call script ceiling
  per run, enforced for every role before script execution. The main adapter
  also retains its own production context check. Inputs/history are never
  silently truncated. Byte counts are not converted into model tokens.
- Worker runs permit two model turns: a native `record` tool call and its final
  summary. Single-Pi main turns similarly contain `subtask` and final output.
  SuperAgent main decisions and worker-result replies each use one model turn.
- The script uses only the runtime's offered input/tasks/tool output. The
  expected task labels are supplied only to the scorer after execution.
- Each delivery is checked against actual terminal reports or returned worker
  outcomes, native completed tool receipts, separate tool-body observations,
  final replies and each task's actual Pi input history. Wrong assignment,
  dropped/duplicated effects, wrong values, missing receipts, false duplicate
  claims, contaminated histories and inconsistent counts fail validation.
- Actual Router input-kind counts distinguish subscription fast paths from
  routed callbacks. The two SuperAgent arms also close/reopen their journals
  after completion, replay identical ingress through the original entrypoints
  and assert unchanged reports/replies with no extra script calls.
- Five repetitions must produce identical normalized observations. These are
  reproducibility checks, not independent statistical samples.

For every arm/repetition the observed result is 10 unique events, two duplicates,
two task histories, six worker invocations, six native worker tool calls, six
record effects and six replies. Every arm makes 24 **script** calls. Single Pi
additionally makes six native `subtask` calls: its total tool count is 12 versus
six in the SuperAgent arms. Two tasks must not be confused with six worker
invocations; six effects must not be confused with twelve total tool calls.

The subscribed arm observes four main `user` inputs, two informational
`task_input` inputs and six `worker_result` inputs. Router-only observes six
`user` and six `worker_result` inputs. Fast-path observations still reach main,
so this workload demonstrates no model-call reduction or performance gain.

`live_model_calls` is zero. Input/output tokens, monetary cost and latency are
explicit JSON **null**, not measured zero, estimates or omitted fields.

## Reproduce offline

Use the installed repository Go toolchain and cached dependencies. PowerShell:

```powershell
$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:GOTOOLCHAIN = 'local'
$env:PIGO_LIVE_TEST = '0'
$env:PI_GO_AGENT_LIVE_TEST = '0'
$env:PI_GO_AGENT_EXAMPLE_LIVE = '0'
go run -mod=readonly ./cmd/pi-superagent-runtime-eval > runtime-protocol.json
go test -mod=readonly -count=1 ./internal/runtimeeval
go test -mod=readonly -race -count=5 ./internal/runtimeeval
```

The command defaults to five repetitions per arm (`-repeats 1` through `5`),
prints the fixture, boundaries and all observed records, and removes its own
temporary journals on exit. It has no provider/model/network option. Failed
runs return an error rather than emitting an apparently successful suite.

Tool barriers test that no effect crosses a closed gate. Cancellation is checked
in every arm, including a single-Pi body that ignores cancellation until released.
This proves joining the admitted body, not durable preservation of failure
receipts: on an error this evaluation aborts and does not export failed-run
snapshots/receipts. Conflicting ingress duplicates are rejected in every arm.

## Remaining gates

The original 300-message workload, cancellations/fanout under backlog, concurrent
arrival schedules, asynchronous single-Pi delegation, tool-service budgets,
first-reply/completion distributions, throughput, semantic routing quality,
real-model token/cost measurements, stochastic prompt-policy comparisons and
pre-M1 history remain missing. Completed-journal reopen is not an interrupted-run
crash/recovery experiment. The full M1–M8 stack and production deployment are not
exercised by these four arms. This is not final M9 or release acceptance.

## Validation

Test-first compile failure was captured before implementation. Independent
read-only review found misleading task/tool counter names and insufficient
result-metadata validation; both were corrected and regression-tested. Review
also verified cancellation against a deliberately uncooperative body and prompted
explicit documentation that failed receipts are not exported.

Verification is on Linux amd64 with cached Go 1.26.10. PowerShell is unavailable
on this host, so execution uses Bash; no Windows/PowerShell validation is claimed.
No real provider/platform, user computer, extra billed execution, CI dispatch,
merge or deployment is included. Exact final-tree checks are recorded in the
Draft PR rather than implying remote CI ran.
