# M9e: four-arm offline conservation with an asynchronous Pi baseline

Based on draft #195, commit `813e414b28515180bd72fd1d85898c4cd613e5e1`,
tree `0aafc888dc43a212f6ae46e777188661a2e3f589`. This development slice is an
internal evaluation harness, not a production scheduler or a new default.
It does not change the library API, journal format, database, provider or CI.

## Question and allowed conclusions

Can the same 300 deliveries conserve their task assignment, native tool
receipts, actual effects, private histories and final replies through four
actual runtime entrypoints, including a persistent single Pi whose subtask
tool admits background work without waiting for its completion?

[M9b](superagent-m9b.md) already exercised four entrypoints serially. Its
single-Pi subtask tool is synchronous: giving it a four-worker ceiling would
not create four-way effective concurrency. Reusing that baseline to claim a
SuperAgent speedup would therefore be misleading. This slice explicitly adds
an **asynchronous** single-Pi host baseline. It does not silently reinterpret
the old baseline or claim prompt quality, lower latency or production parity.

The experiment uses deterministic in-process `StreamModel` scripts. Neither
prompt arm calls a real model. Both single-Pi prompts produce exactly the same
scripted decisions; equality tests their wiring, not a prompt-policy effect.
Successful assignment of explicit synthetic work identifiers does not prove
natural-language understanding.

The earlier roadmap proposed fixing tool durations and recording runtime
latency. That would overstate this slice: the tool only appends an in-memory
effect, SuperAgent has durable journal work, and the single-Pi host does not.
No simulated durations or cross-arm elapsed-time ranking are introduced.
Latency, input/output tokens and cost remain explicit null; live calls are zero.

## Workload and controls

The fixture is **exactly M9c's existing three-phase fixture**, not M9a's
original mixed-arrival workload. Its compact JSON SHA-256 is
`bf7fc1874460d4b7df412306041afe67faf47abbdf5c827020ee111278a06857`.
It contains 300 deliveries, 280 unique source/event identities, 20 tasks,
60 unique actionable events, 220 unique noise events and 20 duplicate deliveries.

1. Twenty requests in four shared chats and 80 noise events.
2. Twenty same-chat corrections and 80 noise events.
3. Twenty task-specific GitHub callbacks, their repeats and 60 noise events.

GitHub event IDs reuse IM IDs to exercise source-scoped deduplication. Completed
phase-1 summaries exist before corrections; the subscribed arm's trusted host
installs task-specific subscriptions from observed task identities. This is a
controlled completed-summary association test, not ambiguous or continuous
traffic while initial work is running.

All arms use the same prebuilt per-phase offered-input sequence, four-worker
hard ceiling, PiWorker and
in-memory `record` tool. Both SuperAgent arms are unbatched, so batching is
not an additional treatment. Processing and completion order may vary; offered
order is not a deterministic execution schedule or equal admission timing.
SuperAgent can execute during Submit calls; single Pi processes an admission
turn between accepting later inputs, then consumes queued results after that
phase's admissions. This extra host policy is part of the baseline. The single-Pi main script
resolves targets from
offered observed task summaries rather than letting the host choose a task
from the expected answer. Expected-work labels belong only to scoring.

A phase drains queued work, admitted bodies and main result processing before
the next phase. Experiment tool bodies complete independently: there are no
M9c four-ticket waves, fake service ticks or requirement to observe four
concurrent bodies. Worker and tool peaks are reported separately and must not
exceed the cap. A separate regression deliberately holds four bodies and proves
that the async host can admit all 20 jobs before releasing them; that test is
not imposed on experiment runs.

## Evidence and asymmetries

- Actual Pi model/tool loops execute. A subtask admission acknowledgment is
  not an effective reply; worker results must return through the persistent
  main Runner. No external message is sent.
- Completed native `record` receipts and separately observed effects are
  correlated by task and source/event identity. A completed receipt is not an
  external commit or an exactly-once side-effect guarantee.
- SuperAgent replies are pending outbox entries; single-Pi replies are returned
  main outputs. Neither is externally delivered.
- SuperAgent journals, subscriptions and replay remain real. The single-Pi
  host's queue, deduplication and snapshots are in memory. Completed-journal
  reopen evidence is specific to SuperAgent, not equal recovery coverage.
- The main Runner retains a transcript; SuperAgent supplies its production
  main context and task summaries. Common byte/call ceilings prevent silent
  truncation but do not make the model contexts or token costs identical.
- Phase conditioning, synthetic identifiers, local scheduling and durable
  versus in-memory storage constrain interpretation. Repetition checks
  correctness, not statistical significance. No winning strategy is inferred.

## Scoring and accounting

Every arm is checked against all 300 admissions and 280 terminal reports,
20 unique task mappings and each task's three-input history. Sixty effects
must match sixty completed native worker receipts and sixty effective final
outputs. Duplicate or missing evidence, cross-source collisions, wrong values,
foreign tasks, contaminated histories and admission text substituted for a
final output fail validation. Corrupting only the scorer's expected-work labels
does not change execution, but the mutated oracle is rejected by validation.

The asynchronous arms additionally retain sixty native `subtask` admission
receipts. These are not worker completion receipts. Script calls are counted
by actual role: each SuperAgent arm uses 120 main plus 120 worker calls;
each async single-Pi arm uses 180 main plus 120 worker calls. The extra sixty
main calls finish the admission tool turns. Both subscription observations and
worker results still reach main, so this fixture shows no subscription-driven
script-call reduction. These counts do not measure tokens or model expense.

The shared limits are 1 MiB per serialized model request, 512 script calls per
run and four worker slots. No context is silently truncated. The suite rotates
arm execution order across five repetitions. Measured peaks need not match
between arms or runs and are not throughput measurements.

Review corrected a tool-concurrency observer that initially covered only its
pre-effect hook, an error-path nil dereference, a vacuous reply-correlation test
and overly loose result metadata validation. Executing the tighter scorer then
exposed an incorrect review assumption: actual SuperAgent result inputs carry
the task in their nested report, leaving the outer TaskID empty. The scorer
now enforces that contract, while the async host's populated outer TaskID must
match its report. No production change was needed. Those failed development
runs emitted no successful JSON suite; final results are from a complete rerun
on the corrected evaluator, not selected successful arms.

## Observed five-repeat run

All four arms completed all five repetitions on the corrected source. Every
run conserved 300 deliveries, 280 identities, 20 task histories, 60 Worker
invocations, 60 worker tool receipts/effects and 60 effective final outputs.
There were no extra live calls, no omitted repetitions and no inferred costs.

Observed worker peaks by repetitions 1–5:

| Arm | Worker peaks | Tool-body peaks |
| --- | --- | --- |
| SuperAgent subscribed | 4, 4, 4, 4, 4 | 1, 1, 1, 1, 1 |
| SuperAgent router-only | 2, 3, 2, 4, 3 | 1, 1, 1, 1, 1 |
| Async single Pi, neutral | 2, 2, 3, 4, 3 | 2, 1, 1, 1, 1 |
| Async single Pi, delegate-first | 3, 3, 3, 2, 3 | 1, 1, 1, 1, 1 |

These observations illustrate why a four-slot ceiling is not a claim that
all arms perform four bodies simultaneously. The memory-append tool is very
short; neither these peaks nor the different admission policies rank useful
parallelism, waiting time or throughput.

- [Per-run summary](benchmarks/superagent-runtime-compare-summary-20261106.json)
  records all 20 runs, their order, counters, controls, source-file SHA-256
  values and artifact hashes.
- [Full raw JSON, gzip](benchmarks/superagent-runtime-compare-20261106.json.gz)
  preserves every fixture entry, admission, native report/receipt, actual
  main input, final output, acknowledgment, effect and private-history key.
  It expands to 6,901,844 bytes; the compressed file is 263,126 bytes.
- Decompressed JSON SHA-256:
  `89221bec6804f5c57cf1fa7cd0478f773c1dc04dc338ebf3f7325842e86f8ce7`.
  Compression was round-trip checked. These are synthetic test records, not
  user conversations or external-service data.

## Reproduce offline

Use the installed repository toolchain and cached dependencies. PowerShell:

```powershell
$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:GOTOOLCHAIN = 'local'
$env:PIGO_LIVE_TEST = '0'
$env:PI_GO_AGENT_LIVE_TEST = '0'
$env:PI_GO_AGENT_EXAMPLE_LIVE = '0'
go run -mod=readonly ./cmd/pi-superagent-runtime-compare > runtime-compare.json
go test -mod=readonly -count=1 ./internal/runtimeeval
go test -mod=readonly -race -count=1 ./internal/runtimeeval
```

The command has no provider, credential or network option. Its output retains
per-run raw observations rather than only aggregate success counts. Failed
runs must return an error; they are not replaced by successful repetitions.

## Remaining acceptance gates

This does not close final M9. The original M9a fixed mixed-arrival schedule,
four cancellations and 120–140 second tool-service budgets are not measured.
M9d's separate cancellation/fanout/crash fixture is not silently combined into
these results. Outstanding work includes controlled continuous-arrival and
service-time comparisons, first-effective-reply/completion distributions,
stochastic prompt effects, real-model tokens/cost, pre-M1 historical evidence
and actual host/platform acceptance. The new asynchronous test host is not a
production alternative orchestrator.

Validation uses the existing remote Linux/amd64 host and cached Go 1.26.10.
PowerShell is unavailable, so Bash equivalents are used; no Windows/macOS or
PowerShell validation is claimed. No user computer, extra paid execution,
real provider/platform, CI dispatch, merge, release or deployment is included.

## Final validation

Passed on the final evaluator source:

- Root `go test -mod=readonly -count=1 ./...` and `go vet -mod=readonly ./...`.
- Full affected-package race: `go test -mod=readonly -race -count=1 -timeout=20m ./internal/runtimeeval ./cmd/pi-superagent-runtime-compare`.
- Separate examples module readonly tests and vet.
- All 20 comparison runs, 16 evidence-corruption cases per arm, oracle
  independence, held-body async admission and uncooperative-body joining.
- CLI rejects repetition counts 0 and 6 without emitting JSON; formatting
  and diff checks pass.
- Independent source review and read-only audit of every artifact summary row,
  repetition/order, native identity contract, source/hash, count and null field.

No full-root race, Windows/macOS, real model or real-host validation was run.
The CLI has no package-specific unit test file; it compiled in the race command
and was exercised by successful and invalid-input executions above.
