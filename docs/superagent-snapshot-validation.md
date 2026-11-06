# Snapshot validation without repeated decoding

This optimization is based on M1c / PR #159 at
`859e0f598e69efa787f26cf76d50d72991f1c67b`. It does not change the public API,
journal format, admission rules, authorization, or execution boundaries.

## Content proof, not permission

Previously one completed main decision decoded the same serialized snapshot
four times: once after callback serialization, once at decision validation,
once at commit, and once at apply. The latter three ran under the coordinator
mutex. Worker terminal publication decoded twice: encoding and apply.

`encodeSnapshot` still serializes callback data and fully decodes that result,
including Agent's stricter tool-argument validation and JSON number handling.
It constructs a private, transition-local result with:

- An immutable string copy of the exact serialized bytes.
- A detached decoded snapshot, never given to a callback or public reader.

Later checks compare the exact bytes and recheck snapshot ownership, then reuse
that parsed value. They do not hash the snapshot. Main decision state, inbox
identity, offered tasks/runs and exact live control targets remain checked at
their existing boundaries. The private result cannot authorize an action.

The raw-byte copy and comparisons are deliberate: a changed payload cannot
borrow validation of different content. This is an O(n) comparison, not an O(1)
permission cache. The decoded value is read-only by ownership within this
package; future consumers must not pass its nested maps or slices to callbacks.

Replay discards any process-local result and validates durable bytes afresh.
The result is unexported and absent from JSON. Public snapshot reads and future
callback inputs still decode their own detached copy. This is not a persistent
transcript cache and does not remove every decode across future turns.

## Scope revision reuse

A separate commit computes toolset scope revision once per frozen directory
and catalog revision once per catalog, rather than once per exposed tool.
The format, AuthEpoch/Identity invalidation, configuration/catalog consistency
checks and execution-time permission guards are unchanged. Nothing is cached
across directory builds.

## Verification

Tests cover invalid JSON shape, excessive size, foreign snapshot ownership,
Agent-rejected tool arguments, one callback serialization, callback/reader
aliasing, large JSON number fidelity, valid-snapshot retention after main
serialization failure, and reopen behavior. Additional direct tests cover
changed raw bytes, wrong owners, changed inbox/state, result-origin action
restrictions, identical serialized journal records, and forced replay with a
stale in-memory result. Existing control race and recovery suites are retained.

The baseline guard tests and benchmark were committed before implementation.
Independent source review found no blocking issue. Validation uses remote
Linux/amd64, Go 1.26.10, GOTOOLCHAIN=local, GOWORK=off, and live-test flags off:

```powershell
go test -mod=readonly -count=1 ./...
go test -mod=readonly -race -count=1 ./...
go vet -mod=readonly ./...
go test -mod=readonly ./superagent -run '^$' -bench '^BenchmarkSnapshotTransition$' -benchmem -benchtime=500ms -count=5
go test -mod=readonly ./superagent -run '^$' -bench '^BenchmarkMainSnapshotChecks$' -benchmem -benchtime=250ms -count=5
# Also run readonly tests and vet from the examples module.
```

No CI workflow, model service, user-machine task, Codex task, merge or deployment
is needed or performed. Windows/macOS and live services are not validated.

## Measurement limits

`BenchmarkSnapshotTransition` isolates serialization and content checks for a
single terminal transition. It excludes journal serialization/fsync, model and
tool execution, scheduling, the next callback's detached input and public reads.
`BenchmarkMainSnapshotChecks` isolates the three validation calls that normally
run under the mutex, not total mutex hold time. Reuse performs zero allocations
in that isolated check benchmark at 64 KiB, 1 MiB and 7 MiB.

The proof adds one immutable raw-byte copy and temporarily retains the decoded
value. Total allocation decreases, but this is not a claim about peak process
memory or end-to-end latency. Shared-host CPU timings are illustrative; use the
benchmarks on the deployment host before inferring capacity.

### Paired transition measurements

Five runs per case, 500 ms each, baseline followed by optimized code on the same
remote Linux/amd64 host (AMD EPYC 9V74, Go 1.26.10). Values below are medians,
not service latency promises. Payload is one large metadata string.

| Path / payload | Baseline ms/op | Optimized ms/op | Baseline B/op | Optimized B/op |
| --- | ---: | ---: | ---: | ---: |
| Main / 64 KiB | 1.927 | 0.513 | 1,439,072 | 491,759 |
| Main / 1 MiB | 30.593 | 9.102 | 24,141,569 | 8,872,154 |
| Main / 7 MiB | 191.285 | 56.241 | 118,513,632 | 50,572,855 |
| Worker / 64 KiB | 0.902 | 0.560 | 757,967 | 492,554 |
| Worker / 1 MiB | 16.914 | 9.089 | 13,581,741 | 8,838,237 |
| Worker / 7 MiB | 103.048 | 54.179 | 70,281,188 | 50,573,427 |

The exact structural change is four content decodes to one for a completed main
decision, and two to one for Worker terminal publication. Journal writes and
next-turn decoding are excluded from these numbers.

The warmed toolset directory benchmark (five 300 ms runs, zero wire calls)
reduced allocations from 295 to 286 for one tool and approximately 20,019 to
19,375 for 128 tools. For 128 tools the median bytes fell approximately
1,314,410 to 1,302,006. No stable toolset CPU improvement was established under
shared-host noise, so none is claimed.

## Integration with parallel development

The optimization branch is based directly on #159 and does not change the
parallel same-chat classification branch. That branch also edits
`superagent.go` and `main_session.go`. During integration, retain both the
private validation result and all new result/control state checks. If its
terminal apply consumes decoded messages to record correction evidence, use
`validateSnapshot`'s returned value read-only there; do not decode the same
transition again or expose that value to callbacks. Rerun full, race, recovery
and correction-evidence tests on the combined tree before publication.
