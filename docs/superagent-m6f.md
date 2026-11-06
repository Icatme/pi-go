# M6f: checkpoint feasibility and validated replay normalization

Based on draft #196, commit `dbba812e4da1af3e23d427781a05c12f8dc53dd5`,
tree `d8eb8f0c831d8bb5d9265cf3e1a05c6ac27e720a`. This slice keeps the
complete original journal and its format. It does not introduce a checkpoint,
pruning, retention policy, database adapter or a new startup API.

## Why a checkpoint is not this slice

[M6d](superagent-m6d.md) identified storage growth and defined the durable
identity contract. The implementation review confirms that a latest snapshot
plus unfinished tasks cannot satisfy it:

- Delivery IDs and public history references depend on original admission
  positions. Complete deduplication also retains original content and admission
  mode, not only a recent key set.
- Reply attempts retain unknown outcomes, confirmed platform message IDs and
  retry/echo-suppression facts. Dropping them can authorize duplicate delivery.
- Main inbox/cursor, reports, review and control barriers, frozen fanout,
  subscriptions, background operations and watchdog observations must survive
  in their original domains and order.
- Runtime callbacks, grants, current ownership epochs, active runs and validation
  proofs are not durable execution permission that can be copied into a cache.

The current `Journal.Log()` contract returns the complete committed log, and
JSONL startup strictly decodes/validates and materializes that log before the
coordinator replays it. A sidecar containing only current coordinator state
cannot remove these storage costs or satisfy public historical APIs.

A sidecar can preserve old-reader compatibility only if the original journal
remains complete. Old binaries may append without updating the sidecar. Checking
length, modification time or the last ID does not verify the historical prefix.
Hashing the prefix still reads every byte. A checksum stored with a mutable
cache detects accidents but does not prove that cached coordinator state was
correctly derived from the log. Skipping old semantic checks must not let a
cache hide an otherwise rejected journal or erase an uncertain operation.

A versioned, preserve-all-identities replay cache is a possible larger feature,
not a small serialization patch. It would need a complete durable-state schema,
state/log binding and semantic validation, safe stale/corrupt fallback, no
inherited execution authority, and equivalence tests at interruption and lease
boundaries. Its benefit must include full prefix verification, cache decoding,
index rebuilding and current backend loading costs. An in-process-only cache
that first performs full replay has a different, narrower reuse case.

None of these designs bounds total storage while all historical identities and
public history remain available. Actual bounded storage needs a separately
specified retention/archive contract and on-demand history/index access. This
slice does not silently introduce one or claim that goal is complete.

## Smaller implementation boundary

A concrete remaining startup cost is visible without inventing new persisted
state. `logState.apply` validates an entry by making a normalized JSON copy,
discards that copy, then makes a second full `LogItem` JSON copy for retention.

The narrow optimization separates structural/semantic checks from serialization
validation only inside `apply`. It validates the original candidate, performs
one full-item JSON normalization with `UseNumber`, then updates indexes after
that succeeds. It retains the existing full validation wrapper for preparation
and batch preflight. JSONL strict decoding, append preparation, persistence,
sync/poison behavior and detached return-value cloning remain in place.

This is not a direct-copy or skip-validation shortcut. The one retained JSON
round trip still normalizes strings, raw JSON, nil/null/empty values and number
representations, and detaches all pointers. Existing invalid-UTF-8 behavior is
not cleaned up as part of a performance change.

Public caller data is normalized when preparing a new entry. Disk data arrives
through strict decoding. The private `apply` helper therefore consumes prepared
or decoded candidates; arbitrary stateful marshalers passed directly to that
private helper are not an additional supported input surface. Public append and
transaction tests cover custom marshaling at the actual API boundary.

Old readers consume exactly the same journal records and append sequence. The
optimization neither changes identity nor grants permission to repeat an action.
Recovery still replays every record and retains the original uncertainty barriers.

## Verification scope

Use differential tests against the original algorithm for prepared/decoded
custom, message, compaction and lane records, plus invalid records, copy isolation,
legacy JSON normalization, public custom marshalers and batch atomicity. Existing
strict/torn-tail, write-failure and coordinator replay-identity tests remain
applicable. Measurements must use the same fixed values/bytes and distinguish
CPU/allocation improvements from storage size and peak memory.

Validation is offline on the existing remote Linux/amd64 host, with cached
Go 1.26.10. PowerShell is unavailable, so Bash equivalents are used. No user
computer, extra paid execution, real provider/database/platform, CI dispatch,
merge, release or deployment is part of this slice.

## Measurements

### Exact-file strict JSONL opening

The baseline binary is built from `dbba812e4da1af3e23d427781a05c12f8dc53dd5`;
the optimized binary differs only by the runtime change shown in the retained
measurement record. Both open the **same** M6d growing-history fixture:
1,000 inputs, 2,002 records and 177,525,225 bytes. SHA-256:
`4f7d825c267b85ff640634bd082e880ec74244d3acbe6dd8090d54dd89b0e46b`.

| Median of three samples | Original | Optimized |
| --- | ---: | ---: |
| Strict opening/validation | 8.130 s | 5.780 s |
| Cumulative allocation | 3,257,910,080 bytes | 2,176,020,224 bytes |
| Allocation count | 256,488 | 198,488 |

All six file hashes and complete decoded Header/Log hashes agree. The file is
unchanged after both variants. Observed reductions are approximately 29% in
opening time and 33% in cumulative allocations for this synthetic workload.
This is **not** an end-to-end coordinator startup or peak-memory measurement.
The coordinator still performs its complete transition replay afterward.

Each sample is a fresh process, with a streamed hash and explicit GC before the
measured call. Hashing, fixture creation, compilation, `Log()` cloning, result
digesting and Close are outside measurement. Go 1.26.10 Linux/amd64 reports
GOMAXPROCS 9. The baseline's three samples precede the optimized three, without
randomization. No other tests/benchmarks from this task overlapped measurement;
the remote host and OS cache are still shared. Hashing makes this a warm-cache
comparison, not cold-disk or production latency.

[Raw samples, source and reproduction procedure](benchmarks/session-jsonl-open-comparison-20261106.txt)
include all six observations. Machine paths are replaced with labeled placeholders; presentation whitespace
is normalized without changing numeric observations.
The large generated fixture/binaries are not committed: regenerate one fixture
once for a new paired comparison, then use those exact bytes for both variants.
New fixture timestamps will differ from this run's hash; do not compare two
independently regenerated files as though their bytes were identical.

### Same-input algorithm comparison

The committed `BenchmarkValidatedReplay` uses fixed decoded inputs and their
identical encoded lines for original and optimized implementations. It covers
custom, ordinary message and mixed records at 100 and 1,000 entries, with five
samples per case. Preparation and equivalence checks are excluded. Selected
1,000-entry individual-line medians, including strict line decoding:

| Profile | Original ms | Optimized ms | Original B/op | Optimized B/op |
| --- | ---: | ---: | ---: | ---: |
| Custom | 48.948 | 35.480 | 16,123,344 | 10,616,962 |
| Message | 53.525 | 36.243 | 20,089,650 | 13,147,365 |
| Mixed | 53.949 | 39.712 | 18,108,112 | 11,885,708 |

A separate batch comparison includes unchanged preflight and returned-entry
cloning. The test-only original oracle's returned-slice allocation order was
corrected to match production, and every batch case was rerun. The earlier
batch samples are excluded, rather than being mixed with the corrected results.
All [decoded/line samples](benchmarks/session-validated-replay-lines-20261106.txt)
and [final batch samples](benchmarks/session-validated-replay-batch-20261106.txt)
are retained. `log-B` denotes logical per-item JSONL bytes, excluding the header
and batch envelope; it is not a full-file size.

PowerShell reproduction with cached dependencies and live flags disabled:

```powershell
$env:GOTOOLCHAIN = 'local'
$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:PIGO_LIVE_TEST = '0'
$env:PI_GO_AGENT_LIVE_TEST = '0'
$env:PI_GO_AGENT_EXAMPLE_LIVE = '0'
go test -mod=readonly ./agent/session -run '^$' -bench '^BenchmarkValidatedReplay$' -benchtime=100ms -count=5 -benchmem
```

The optimization reduces repeated normalization work. It does not reduce
journal bytes, the number of retained identities, the need for complete replay,
or the amount of history kept available. B/op and the opening allocation deltas
are cumulative allocations, not retained memory or RSS.

## Final validation

Passed on the final runtime and test source:

- Root readonly tests and vet, including the existing coordinator replay-identity,
  uncertain execution/delivery and history-reference regressions.
- Affected race: `agent/session`, `superagent/...` and `cmd/pi-superagent`.
- Separate examples module readonly tests and vet.
- Differential prepared/decoded records, pointer isolation, 22 invalid cases,
  normalization decode failure, invalid-batch atomicity and public changing
  marshaler append/CompareAppend/read/reopen checks.
- All final benchmark cases, exact-file baseline/current verification, gofmt
  and diff checks. Independent source/test/oracle review found no blocker.

Full-root race, Windows/macOS, real backend and cross-host validation were not
run. Existing temporary-file SQLite tests are regression coverage, not a new
production database test or adapter change.
