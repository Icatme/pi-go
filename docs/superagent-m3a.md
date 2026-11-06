# M3a: admission gates and bounded main backlog batches

This development slice is based on M2c / PR #165. It is not a merged release,
a production throughput claim, or the historical retrieval portion of M3.

## Host entry points

`OpenMainBatch(ctx, path, workers, MainBatchOptions, MainBatchRouter, Worker)`
uses the existing persistent main session, worker scheduler and exact controls.
`MainBatchJob.Inputs` is an ordered contiguous inbox prefix. Each
`MainInputDecision` must contain the exact `InputID` and one `MainDecision`.
The returned list may be reordered: the coordinator maps IDs back to inbox order.
`PiMainBatchRouter` runs one read-only model turn against the persistent snapshot
and parses `{"decisions":[{"input_id":"...","decision":{...}}]}` strictly.
Duplicate fields, aliases, unknown fields, duplicate/omitted/extra IDs and invalid
actions fail the entire group. Existing `OpenMain` and `PiMainRouter` remain
single-input APIs and read journals created by either API in this version.

`MainBatchOptions` requires `MaxItems` from 1–64 and `MaxInputBytes` from
1 KiB–1 MiB. The latter bounds the actual JSON encoding of the Inputs array,
including escaping and array syntax. It does not include task candidates,
system instructions or history; `PiMainBatchRouter` independently checks its
full serialized context budget before a model call. An over-budget first input
gets a durable failure, rather than being truncated or blocking every later input.
There is no debounce or timer: the scheduler starts available work immediately.
Events arriving during that inference form subsequent bounded batches.

The external-package `ExampleOpenMainBatch` is an executable Go host example.
No CLI or live model is required.

## Authenticated admission boundary

`Admit(policy, envelope)` is an opt-in host adapter boundary, available with
OpenMain and OpenMainBatch. Policy rules match exact source/principal/group
**tuples**, not a cross product of independent allowlists; there are no wildcards.
An empty group means a direct conversation. Empty rules deny all inputs.
The version, original envelope, decision, reason and frozen Thread recipients
are stored in one admission record and returned by `Admissions`.

The host must authenticate `Principal`, `Group`, source and routing association,
and determine explicit-request intent. Do not copy these facts from an incoming
body, tool result or model output. This API is not a transport authenticator.
An accepted input requires an allowed tuple and one of:

- An explicit request with `Kind: "request"`.
- A trusted `TaskID` association with an existing task.
- `ToThread: true` and at least one currently authorized Thread subscriber.

Noise never reaches the router or worker. Unassociated signals and requests
without established explicit intent are recorded as ignored. A signal to an
empty subscription does not fall back to the main session. Malformed envelopes
return errors without admission. After the policy permits an input, unknown
explicit task IDs also return errors; denied inputs do not reveal task existence. Admission
and Thread recipient selection occur under the same lock, eliminating a check/
submit race. Later subscribe/unsubscribe changes affect only later ingress.

The [history admission fix](superagent-history-admission.md) enforces the same
boundary on indirect `history_read` access: rejected inputs, noise, their reports
and links stay outside the public catalog. Host audit APIs retain their records.
See that note before upgrading a journal that may already contain leaked model
history; rebuilding an index does not sanitize an existing model snapshot.

The source/event dedup key is unchanged. A repeated full envelope returns the
original receipt, even if the policy/version or current subscriptions changed.
A changed principal, group, intent, content or association conflicts with that
identity. Do not change the policy merely to retry an ignored event; use a new
host-authorized event ID. Known outbox echoes remain suppressed by the existing
reply identity filter and return a duplicate receipt without a new audit record.

`Submit`, `SubmitTask`, `SubmitThread` and host controls remain privileged APIs;
they intentionally do not consult a gate policy. An adapter enforcing a policy
must consistently use Admit for its untrusted ingress. The main model receives
the existing bounded input representation, not credentials or policy rules.

## Atomic decisions, authority and errors

The callback receives detached inputs and frozen candidate/run lists. The
coordinator retains its own offered IDs. Same-batch tasks have no offered ID:
attach can only select an existing offered task. Every input retains its own
kind restrictions, including informational results and mandatory handback
clarification. A model must clarify an attribution that needs an as-yet-created
task rather than inventing its ID. Same-batch inputs can queue to one existing
task; execution retains the original input order.

Each exact RunTarget can receive at most one control in a group. Offered runs
are checked again at commit; worker completion or host Abort makes a proposed
control stale and rejects the whole group. Native host Abort/Steer and already
associated worker work remain independent of blocked main inference.

The whole decision group is validated in an isolated reducer state, including
sequential task numbering and legacy Thread bindings. A single
`main_batch_decided` journal append stores all decisions and one final snapshot.
No worker or outbox action occurs during validation. Only after commit may the
existing scheduler execute pending actions. Snapshot validation remains bound
to immutable encoded bytes, and replay obtains a fresh validation proof.
The snapshot is copied once per group reduction, not once per input.

Callback error/panic, malformed output, stale authority or invalid snapshot
produces one `main_batch_failed` record and a durable `MainFailure` per input.
The original inbox data remains in the journal, the snapshot does not advance,
no partial actions execute, and the scheduler advances to later inputs. Private
in-process causes remain available through `MainFailureCause` and are not
published as replies. The automatic retry bound is **zero**. A host can inspect
failures and explicitly admit a new authorized event; unchanged event IDs remain
deduplicated. Cancellation before any commit leaves the whole group unconsumed
for read-only inference after reopening. Storage failure remains fatal and is
never converted into a successful failure-record append.

## Migration and boundaries

Old journals retain their identities, provider sessions, review barriers,
subscriptions, deliveries and replies. There is no rewrite. New admission and
batch transitions cannot be understood by older binaries: stop the exclusive
old writer before upgrade and retain a pre-upgrade backup for rollback. Do not
run mixed-version writers or remove new records to force downgrade.

The isolated validation state still copies reducer-owned historical collections.
Its cost is O(journal history), not a history-independent fast path. The small
benchmark isolates snapshot-copy behavior: a 1 MiB snapshot with 64 inputs
allocates approximately one snapshot per validation, rather than 64 copies.
It is not an end-to-end throughput or model-cost benchmark. Historical spill,
authorized reference traversal, indexes beyond the recent 128 candidates, bounded
history memory, retry scheduling and production load testing remain later M3 work.
No real transport, external login, billable model, user machine, CI dispatch,
merge or deployment is involved in this slice.

## Verification

Tests are offline, on remote Linux amd64 using official Go 1.26.10. Tests-first
compile failures proved the missing new API; final tests cover:

- Immediate first input, backlog count limits, JSON escaping byte limits,
  oversized-head failure, ID reordering and distinct same-batch delegations.
- Missing/duplicate/unknown IDs, callback mutation, mixed-kind restrictions,
  late reducer rejection and zero partial effects.
- Native exact cancellation and known-task progress during blocked inference;
  stale controls after either host Abort or durable worker completion.
- Per-input failures without retry loops, closed uncommitted batch recovery,
  and every persisted crash boundary with original admission identities.
- Source/principal/group tuple and intent gates, frozen fanout recipients,
  changed-policy duplicates, detached audit views and recovery.
- Strict Pi batch JSON, one inference, persistent history, total context budget,
  and no inherited tool/execution hooks or partial failure snapshot.

Run the full repository tests, vet and race suite; the examples module tests/vet;
and repeated focused batch/admission/Pi/control/legacy cases before publishing.

Final validation for this slice passed: full `go test -mod=readonly -count=1
./...`, `go vet -mod=readonly ./...`, full `go test -mod=readonly -race -count=1
./...`, examples-module readonly tests/vet, and focused batch/admission/Pi/
control/Thread/delivery/legacy cases repeated with race 20 times. Independent
final review approved the slice and separately ran focused race tests three
times. Gofmt and diff whitespace checks are clean. Windows, macOS, live
providers, real transports and production load remain unverified.
