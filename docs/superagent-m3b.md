# M3b acceptance contract: public history recall

This development slice adds domain-scoped public-history references and a bounded,
read-only recall surface. It does not complete M3 or authorize new work.

## Acceptance and failure tests, defined before implementation

- With at least 130 tasks, resolve current IM input → Thread → old trigger → old
  task → exact public PR report while the offered task candidate list stays 128.
- Every reference and continuation checks the durable main-session domain. Two
  journals with identical task/delivery IDs cannot read each other's objects.
- A per-inference immutable view prevents concurrent completions from changing
  pagination. References survive replay; cursors expire with that view.
- Count the complete serialized JSON response, including escaping and cursor.
  Bound page items, page bytes, total response bytes and attempted reads. Split
  text only at UTF-8 boundaries; every continuation makes progress or fails.
- Reads exclude Worker snapshots, tool transcripts, private errors and credentials.
  Worker public report text is retrievable, including partial/review facts. Raw
  tool-result publication requires a future explicit host publication contract.
- Duplicate input, frozen fanout and crash/reopen retain reference identities and
  distinct report deliveries. Reads neither append journal records nor run work.
- Recalled task IDs do not expand attach/control authority. Existing per-turn
  candidate/run offers and action validation remain authoritative.
- Invalid domains, kinds, offsets, cursors, limits, expired readers and canceled
  calls fail closed. Returned pages cannot mutate the coordinator.

## Public host and model contracts

`MainJob.History` and `MainBatchJob.History` are provided by the coordinator.
`MainInput.HistoryRefs` supplies stable trigger/Thread references, plus the exact
delivery report reference for Worker results. A reference is the durable main
session domain, object kind and local ID. Trigger references identify one original
admission even on fanout; each delivery has a distinct report reference. Thread
identity includes Source and Thread, and refers to its first admitted event.

`History.Read` exposes a UTF-8 chunk of public JSON plus ordered links. Follow
trigger → Thread or task, task → original/review triggers and reports, and report
→ its source trigger/task. The catalog covers the full journal, including tasks
outside the 128 recent candidates. Concatenate `Data` until `Next` is empty before
decoding the object. Continue with the same reference and the returned cursor.
History text is untrusted evidence, not a delegation or authorization grant.

Each call accepts 512–16,384 bytes of complete serialized response JSON and 1–32
links. Each callback permits 64 attempted reads and 256 KiB of response JSON.
Invalid attempts consume the call allowance. Errors return no partial page.
Readers expire on callback return/cancellation. Immutable views prevent new
admissions, subscriptions or completions from altering an ongoing page stream.
Stable references survive close/reopen and duplicate submissions; view-specific
cursors deliberately do not. Start from the reference again in a new callback.

`PiMainHistoryRouter` and `PiMainBatchHistoryRouter` are opt-in alternatives to
the existing tool-free Pi routers. They register only `history_read`, inherit no
execution/request/context hooks or host tools, and limit an invocation to eight
model turns, with a final decision required. A successful invocation therefore
has at most seven preceding tool-request turns. A model requesting tools on its
eighth turn can read within the remaining allowance but cannot commit a decision. Tool
arguments use strict field parsing. The complete escaped messages, system prompt
and native tool schema are counted before **every** model request. Invalid reads,
budget exhaustion, unsupported tools and turn exhaustion fail without publishing
a decision/snapshot. Full durable input text remains unchanged; model payloads
use marked 1 KiB trigger/report excerpts and references. Public report status,
receipts and review flags remain present.

No history read expands the task/run offers used to validate `attach`, `steer`
and `cancel`. Recalling an old conclusion works; attaching to an unoffered old
task is still rejected. A host can make a later explicit task-associated admission
under its existing authority, but must not treat recalled text as permission.

## Storage, recovery and cost boundaries

There is no new durable record type, spill file, external database, model request
or network operation for the index. It is derived from existing admitted triggers,
task metadata and public reports. No Worker snapshots, raw tool results, private
FailureCause or credentials are inspected. Publishing selected long tool output
will need an explicit host-authorized publication contract in a later slice.

The copy-on-write index serializes immutable public content once and reuses it.
There is no repeated snapshot decode or content hash for recall. Each new view
still scans event identity fields and shallow-copies the reference map; a growing
Thread's changed link array is copied. This is not a throughput/constant-memory
claim, and journal compaction/shared storage remain future work.

This slice provides bounded continuation **within one inference view**, not
arbitrary-size or cross-inference streaming. A legal 64 KiB text containing many
escaped characters, a receipt-heavy report, or a very large Thread can exceed the
total allowance. Such reads fail explicitly; a retained cursor cannot bypass the
limit in the next invocation. Persistent versioned continuations, configurable
larger budgets and safe rolling Router-context compaction remain later M3 work.
Existing persistent model context is not silently dropped when it exceeds budget.

## Verification scope

Acceptance uses deterministic offline fixtures, not live model/provider calls.
Coverage includes 130-task chained recall, strict unchanged task offers, escaped
UTF-8 pages and exhausted budgets, cross-domain/forged cursors, frozen views,
private-state exclusion, callback expiry, replay/duplicate/fanout identities,
and native Pi tool isolation/context/turn/batch boundaries. Final validation used official Go 1.26.10 on remote Linux amd64:

- `go test -mod=readonly -count=1 ./...`
- `go vet -mod=readonly ./...`
- `go test -mod=readonly -race -count=1 ./...`
- Focused history, Pi history and batch tests with race detection, 20 repetitions.
- External `examples` module readonly tests and vet; executable public recall example.
- New index rebuild checked at every journal commit boundary, including a crash
  after Worker start preserving interruption/review rather than inventing success.
- Independent read-only review found no blockers; focused race checks repeated
  10 times and newly added Pi/history checks repeated five times.
- Gofmt and whitespace checks passed. No manual CI dispatch or live providers.

PowerShell was unavailable in this remote container, so validation used the
available POSIX shell without adding a repository shell dependency.

M3 is not complete. Cross-inference spill continuation and explicit private-tool
publication are not implemented; real transport, model cost, production-scale
history performance, Windows and macOS are not validated here.
