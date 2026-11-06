# Same-chat tasks with single-decode snapshots

2026-11-05. Isolated integration of draft #160 and draft #161. This is an
unmerged development stack, not a release or deployment.

## Exact inputs and scope

- Base: #160, `codex/superagent-m2a`,
  `4670b5f556724fd2f0434e3e772eafdc883fedfc`, tree
  `a8449c7205b943c09a576566df627c030cb40b54`.
- Optimization evidence: #161, `perf/superagent-snapshot-once`,
  `420209aed907fbb868151990cc5d7bdbddd8f647`, tree
  `a416270b4cedd404a02af99f78e9b48bf17b964e`.
- Both depend on #159. All three optimization commits are retained: baseline
  snapshot safety/benchmarks, toolset revision reuse, and snapshot validation
  reuse. Neither input branch nor their shared base is changed.

The integration targets #160 for a reviewable stack. #161 remains separate
source and performance evidence, not an already merged dependency.

## Integration boundary

The terminal Worker transition retains `validateSnapshot`'s decoded value and
passes it read-only to `recordControlInputs`. It does not decode again to inspect
correction metadata. Main's current inbox/state and offered task/run checks,
read-only observation restrictions, exact correction identity, effects-review
barriers, and no-reinjection behavior remain unchanged.

The private proof still binds an immutable copy of encoded bytes to a detached
parsed snapshot. Replay discards it and validates the saved bytes. Public reads
and subsequent callback input remain detached; no persisted proof or public API
is introduced. See [the original optimization](superagent-snapshot-validation.md)
for security and measurement limits.

The correction matrix now exercises the live proof path and then reopens the
journal. Each terminal case asserts status and input acceptance before close as
well as after replay. It also verifies that evidence collection does not mutate
the private decoded snapshot. Wrong role, ID, origin, task, run, text and missing
metadata remain uncertainty, not accepted input. Interrupted accepted input
continues to preserve the separate effects-review barrier.

## Combined-tree verification

Official Go 1.26.10, remote Linux/amd64; `GOTOOLCHAIN=local`, `GOWORK=off`,
`PIGO_LIVE_TEST=0`, `PI_GO_AGENT_LIVE_TEST=0`. Checks performed:

```powershell
go test -mod=readonly -count=1 ./...
go vet -mod=readonly ./...
go test -mod=readonly -race -count=1 ./...
go test -mod=readonly -race -count=20 ./superagent -run 'TestMain(SameChat|Task|ChatUpgrade|Correction|Informational)|TestPiMainCorrection|TestSnapshot|TestValidatedSnapshot|TestInvalidEncodedMainSnapshot|ExampleSuperAgent_SubmitTask'
go test -mod=readonly -race -count=10 ./agent/toolset -run 'TestDirectoryRevisionTracksFrozenScope|TestScopeDriftDuringPermissionBlocksDirectoryExecutor'
# From examples/:
go test -mod=readonly -count=1 ./...
go vet -mod=readonly ./...
```

The two-task acceptance scenario holds logs task A while tests task B starts,
finishes and replies; a correction reaches only A. Reopen and duplicate inputs
preserve task, correction and reply identities. The public SubmitTask example
also passes. Independent source review found no blocking issue; its suggestion
to check live correction evidence before replay was incorporated.

Original snapshot transition, locked content-check and warmed-directory
benchmarks were rerun on this combined tree. These are offline microbenchmarks,
not service throughput or end-to-end latency measurements. Their zero-provider
scope and original exclusions are unchanged.

Five-run combined-tree medians (500 ms per transition case): main 1 MiB was
10.521 ms/op and 8,905,605 B/op; Worker 1 MiB was 9.478 ms/op and 8,967,775 B/op.
The three isolated main checks again used 0 B/op and 0 allocs/op with reuse at
64 KiB, 1 MiB and 7 MiB. The warmed 128-tool directory used a median 19,375
allocs/op with zero wire calls. These reruns confirm the retained fast path;
they are not paired measurements against #160 and do not establish a change
in service performance or a CPU regression relative to earlier shared-host runs.

No user-machine access, Codex task, paid service, live provider, external
message, CI dispatch, merge or deployment was used. Repository workflows are
manual-dispatch-only. Windows/macOS and live platform behavior remain untested.
