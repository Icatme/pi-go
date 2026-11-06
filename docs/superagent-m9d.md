# M9d: bounded mixed traffic, exact cancellation and crash conservation

2026-11-06. Test-only follow-up to draft #187, exact remote base
`5238a362b2732c538f91c465396bf062988c654e`. No production API, provider,
dependency, journal schema or CI configuration changes.

## What this verifies

The fixture uses actual `OpenMain` / `OpenMainBatch`, `PiMainRouter` /
`PiMainBatchRouter`, `PiWorker`, native receipt tracking and JSONL persistence.
Models are in-process deterministic scripts with a 1 MiB serialized request
budget and a 512-call guard. Tools append to a separate local file and `Sync`
before reporting a native local commit. No model or external platform is called.

Each arm admits **35 submissions, 23 source/event identities, eight tasks and
30 terminal delivery identities** before reconciliation. Seven GitHub events
fan out to the same two subscribed tasks; each recipient retains distinct
delivery/report/reply identity and truthful receipt evidence. The six other tasks share one chat
with both original tasks, so chat identity alone must not join their histories.

1. Bootstrap two tasks, A and B, then subscribe both to one fixture PR thread.
   This bootstrap is the only global idle boundary before mixed execution ends.
2. Deliver one shared update. Both native tool bodies enter concurrently. A
   waits before its write; B performs its write, marks it committed, then waits
   before tool completion.
3. Submit a main-session correction for B's exact active invocation. Assert it
   is only queued. Block an actual main-model call and admit six further fanout
   events and their duplicates, establishing real task and router backlog.
4. Reject an obsolete A run target. In the cancellation arm, `Abort` only A's
   shared invocation. Its intentionally late tool writes **after cancellation**
   and exits. A must settle while the main model and B are still held, preserving
   a completed, committed native receipt and a `needs_review` interruption.
5. Release the router. Admit six new requests, six noise events and six more
   duplicates, paced by each independent task's completion. Every new task must
   finish before B's body is released. There is no `WaitIdle` between these
   arrivals. Batch mode must observe at least one actual multi-input model call.
6. Release B. Its correction is consumed at a native input boundary and is
   positively evidenced in B's saved snapshot only. Its six queued follow-ups
   complete serially. A's six follow-ups remain blocked by its review barrier.

The cancellation arm has **16 fsynced local effects**, 30 terminal reports and
22 pending outbox replies at this boundary. A canceled tool's committed write is
never described as rolled back; B's independent effects and correction survive.

## Interrupted process variant

An isolated test-binary child executes the same admissions. Instead of canceling
A, it releases A's tool and injects an offline model failure after the committed
write. A's failed report, native receipt and partial private snapshot are saved.
B remains held after its own committed file write, with its correction queued.
The child syncs a checkpoint containing observed reports/replies/snapshots and
calls `os.Exit(73)` without `Close` or deferred cleanup.

The parent checks that exact exit code and rejects **all unexpected child
diagnostics**, including race warnings that an explicit exit could otherwise
hide. It opens the journal with a fresh coordinator. B's interrupted invocation
requires review, its six queued follow-ups are blocked, and its queued correction
becomes `not_delivered`. The external fixture file proves B's write happened,
but B has **no persisted terminal native receipt or partial shared-run snapshot**.
Recovery must not invent either. A's already recorded facts remain unchanged.
This arm has **10 fsynced effects**, 30 reports and 22 pending replies.

After checking physical effects, the trusted test host reconciles A, and B in
the crash arm. Exact effect/report snapshots are captured **before** Reconcile.
Old blocked instructions never execute. Two fresh explicitly associated events
produce exactly one correctly attributed committed receipt and file record each:
18 effects after cancellation, 12 after the crash. Completed reopen and duplicate
resubmission then leave reports, replies, controls, snapshots, script calls and
effect counts unchanged.

## Evidence and oracle

- Raw admission counts and frozen fanout recipients; unique delivery identities;
  exact source/thread/task association; terminal state and review barrier.
- Native receipt token/domain/call/tool/commit/completion facts against actual
  fsynced effects, including the deliberately unknown interrupted B receipt.
- Pending replies with unchanged authoritative reports, exact correlation and
  text quoting the actual failure/review/commit facts. No messages are sent.
- Private input order, task isolation and the durable correction marker;
  checkpoint evidence unchanged after process recovery.
- Fourteen corrupted-evidence cases per normal arm and fifteen per crash arm:
  missing/duplicate/misassigned reports, lost commits/effects, duplicate or empty effects,
  wrong or missing replies, replies for ignored inputs, mismatched decision IDs,
  misdirected correction, contaminated/missing snapshots,
  and invented crash receipts must fail the oracle.

The test-first target failed to compile before its fixture existed. An initial
fixture check incorrectly omitted blocked-delivery replies from its checkpoint
count; that harness assertion was corrected. Neither is a production defect.
Independent review requested an observed multi-input batch and exact
pre-/post-Reconcile effect checks; both are now assertions. Final verification
and exact-tree publication are recorded in the draft PR, not inferred from a
prior tree's successful checks.

## Reproduce without network or paid execution

Use the installed toolchain and already cached dependencies. PowerShell:

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
go test -mod=readonly -count=5 -run '^TestMixedRuntime' -v ./internal/runtimeeval
go test -mod=readonly -race -count=3 -run '^TestMixedRuntime' ./internal/runtimeeval
go test -mod=readonly -count=1 ./...
go test -mod=readonly -race -count=1 -timeout=15m ./...
go vet -mod=readonly ./...
Push-Location examples
go test -mod=readonly -count=1 ./...
go test -mod=readonly -race -count=1 ./...
go vet -mod=readonly ./...
Pop-Location
```

The crash helper skips unless invoked by its parent test; the two parent crash
subtests actually run it. PostgreSQL remains gated/skipped. On the existing
remote Linux/amd64 host PowerShell is unavailable, so Bash equivalents are used
with cached Go 1.26.10. Windows/PowerShell are not validated here.

## Boundaries

This is a **bounded, completion-paced interleaving**, not a constant-rate,
randomized or unbounded continuous-load benchmark. The new 35-submission fixture
does not replace or enlarge M9c's 300-delivery phased fixture. It closes selected
protocol gaps under backlog, not the four-strategy M9 experiment. Six independent
completions while another task is gated are a scheduling/correctness fact, not
throughput, latency, prompt-quality or model-cost measurements. Live model calls
are zero; tokens, cost and latency are unmeasured, not assigned invented zeroes.

The process exit covers one controlled boundary on a shared local filesystem,
not host power loss, disk loss, network partitions, every journal commit boundary,
cross-host takeover, real device behavior or production-platform acceptance.
The separately synced effect file is not atomically committed with the journal;
exactly-once external effects are not claimed. Repeated runs check reproducibility,
not statistical significance. The full M1–M8 stack, asynchronous single-Pi
baseline, historical pre-M1 data and final M9/release gates remain open.

No user computer, Codex/extra paid execution, live service, manual CI dispatch,
merge, auto-merge or deployment is used.
