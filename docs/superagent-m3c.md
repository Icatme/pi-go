# M3c: authenticated public-history continuation

Development slice on draft #174, exact remote base
`1623359f63551798b2d47b8d4e42069935d7c08b` (tree
`4ca9d2069b73789c1e17fc8470f49457139abf98`). This is not a release, full M3
completion, durable spill storage, or permission to publish private Worker data.

## Verified gap and tests-first evidence

M3's plan calls for oversized public content outside the model window and bounded
continuation by reference. #167 already indexes full public admitted triggers,
threads, task metadata and reports outside the model prompt, with marked inline
excerpts and read-only pages. No second blob store or tool-publication API is
needed for this slice. Its cursors, however, were tied to one callback. A valid
64 KiB text consisting mostly of escaped characters can exceed the 256 KiB
serialized response budget, so starting again cannot complete the read.

Three new regression tests failed before runtime changes:

- A returned cursor could not continue an unchanged object in the next callback.
- The old canonical base64-JSON cursor allowed a caller to alter valid Text/Link
  offsets and manufacture apparent completion. This was a continuation-integrity
  weakness, not a private-data disclosure or per-callback budget bypass.
- After the journal ownership context was canceled, a read could still succeed
  before the asynchronous cancellation watcher reached the coordinator context.

## Read and authorization contract

`MainHistory.Read`, `HistoryRequest` and `HistoryPage` retain their public shape.
A successful page's `Next` is now an opaque authenticated token. Preserve it
unchanged with the same `Ref` and accumulated data/links. A fresh callback from the
**same live coordinator** may continue that token only when the referenced
public JSON and complete ordered links still match its exact revision.

Each read checks its current reader, caller context, coordinator context, direct
leased-journal lifetime when present, authorization domain, object lookup, token
integrity, exact object revision, offset bounds and UTF-8 boundary. A cursor is
not a grant. It never adds task offers, attach targets, control rights or access
to other domains. Every linked reference is checked again when it is read.

The limits remain 512–16,384 serialized response bytes, 1–32 links per request,
and 64 attempted reads / 256 KiB per callback. Invalid attempts consume the call
allowance. Errors return no partial page. The old reader expires when its callback
returns; obtain the next reader only from a later authorized callback. The host
controls whether another input/callback is appropriate. No timer, new event,
extra model call or automatic retry is introduced by continuation.

Concatenate Data and ordered Links only from the matching stream. On unavailable
cursor, discard the partial reconstruction before restarting from the stable
reference. A growing thread, task assignment or newly published report can change
links and invalidate a prior cursor. Changes to unrelated nodes do not. An active
older reader keeps its own immutable snapshot until it expires, so concurrent
admissions cannot silently mix its pages.

## Integrity, leases, restart and compaction

The fixed-size versioned token authenticates the entire requested reference,
SHA-256 of the exact public document plus ordered links, and both offsets with
HMAC-SHA-256. Its 108-character envelope leaves useful room at the 512-byte page
minimum. Digests are computed once per changed copy-on-write node, after its links
are complete; unchanged nodes reuse them. The random key belongs to one catalog /
coordinator lifetime. It is not written to the journal, Worker snapshot, model
snapshot or public page. Closing a reader clears its own copy.

Restart and lease takeover create a new key and reject old tokens, even when
session domain, reference and content are identical. Stable references remain
readable from offset zero. The direct ownership-context check closes the
asynchronous propagation gap; it follows the trusted leased backend's ownership
context contract, rather than performing a new database transaction for every
page. Existing fencing still controls all writes and effects.

There is no journal schema or ID migration. This does not implement journal
compaction, bounded index retention, durable cursor resumption or a compaction
protocol. Future compaction must preserve reference identities and rebuild/rotate
this catalog; it must not reuse a stale digest/key for different content.

## Model and privacy boundaries

The opt-in Pi single/batch adapters still expose only `history_read`, enforce
context size before every request and allow at most eight model turns. A cursor
can be retained in a successful snapshot for a later real callback; the adapter
does not schedule that callback or compact accumulated tool results. Context
exhaustion still fails explicitly. Host/custom routers can accumulate pages
outside their model snapshots. This is not unbounded model-context recall.

Worker snapshots, raw tool results, private FailureCause values and credentials
remain excluded. Existing public report status, receipts and review facts remain
visible. Explicit host-selected tool publication is separate, unfinished work.

## Offline verification

The verification environment is the existing remote Linux amd64 workspace with
Go 1.26.10, cached modules, `GOTOOLCHAIN=local`, `GOWORK=off`, `GOPROXY=off`,
`GOSUMDB=off`, and all live-provider/example-test flags disabled. PowerShell is
unavailable, so checks use the existing POSIX shell without adding a shell
dependency to the repository.

Targeted coverage includes exact escaped/UTF-8 reconstruction beyond both
per-callback budgets; minimum pages; field/MAC/reference forgery; legacy-token
rejection; changed data, link target, summary, relation, order and append;
unrelated changes; frozen readers; restart; real shared-SQLite takeover during
held inference; direct canceled ownership before watcher propagation; and
read-only journal equality. Existing cross-domain, private-state, task-authority,
model-turn/context and recovery tests remain in the suite.

Final validation passed:

- `go test -mod=readonly -count=1 ./...`
- `go vet -mod=readonly ./...`
- `go test -mod=readonly -race -count=1 ./...`
- History/Pi/public continuation race tests, 20 repetitions.
- Separate `examples` module readonly tests and vet.
- Gofmt and whitespace checks.
- Independent read-only review and an independently run focused suite found no
  blocker. Its one test-comment precision issue was corrected: the synthetic
  overlength-reference envelope fixture is not a production identity example.

Public API acceptance covers real `OpenMain` and `OpenMainBatch` callbacks for
oversized triggers and reports, expired readers, exact reconstruction, private
snapshot exclusion and reopen. Both native Pi adapters retain and reuse a cursor
through actual `history_read` tool messages in later callbacks; their existing
messages are preserved, not implicitly compacted.

No live model/provider/device/service call, production
database, user computer, credentials, Codex or extra paid execution, manual CI,
merge or deployment is part of this slice. Windows/macOS execution is not claimed.
