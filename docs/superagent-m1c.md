# M1c: reliable reply delivery

This slice builds on [M1b](superagent-m1b.md). M1a already supplied a durable
outbox, stable reply/decision identities, direct memory acknowledgement and
authoritative Worker facts. This change adds external send ownership,
verification after an unknown outcome, safe retry, and identity-based echo
filtering. It does not install or contact an external channel.

## Host contract

1. Read `Replies()`. Each reply has its original host-owned Source and Thread,
   stable ID, DecisionID, InputID and any Worker Report. Only `pending` replies
   can be claimed. All reads, including `Attempts`, are detached copies.
2. Call `delivery, err := BeginReplyDelivery(reply.ID)` before sending. A
   successful call syncs a `reply_claimed` record and returns
   `ReplyDelivery{ReplyID, Attempt}`. Concurrent callers have one winner. On
   any error the handle is zero and the caller must not send.
3. Send to the reply's Source and Thread outside the coordinator. Use the
   stable **ReplyID**, not Attempt, as the transport idempotency key. Attempt
   is an internal callback fence, not permission to send to a new destination.
4. Call `ResolveReplyDelivery(delivery, result)` with trusted transport facts:
   - `delivered`: requires a valid platform MessageID; the reply becomes
     `acknowledged`. This means the platform confirmed delivery/creation,
     not that a human read it.
   - `unknown`: keeps `needs_review`. A timeout, lost connection, process exit
     or a non-authoritative "not found" response is not safe retry evidence.
   - `not_delivered`: requires a nonempty verification Note and no MessageID.
     The host must have proved that this send ended without delivery and cannot
     still complete. Only then does the reply return to `pending`; a new claim
     gets Attempt+1 while retaining the same ReplyID.
5. If an acknowledgement or own-message echo arrives first, authenticate it
   and correlate it to the original handle, then call
   `ObserveReply(source, thread, delivery, messageID)`. It checks the exact
   destination and performs the same durable delivered transition. It never
   enters the main inbox or the bound Worker.

`AcknowledgeReply(ID)` remains the simple memory-reader path. It is permitted
only when the reply has **never** had an external delivery attempt. It cannot
clear an unknown outcome or bypass retry ownership. `ExampleOpenMain` shows
direct reads; `ExampleSuperAgent_BeginReplyDelivery` uses an in-memory channel
to demonstrate the external adapter contract with no service or credentials.

## Echo and callback identity

The delivered outcome and platform MessageID are committed together. `Submit`
filters that exact `(Source, Thread, MessageID)` before both ordinary admission
deduplication and the known-thread Worker fast path. For this check, the trusted
adapter must normalize an own-message echo's Trigger.ID to the platform message
ID. Same text, an unrelated source/thread, or a reply's originating EventID is
not sufficient. The identity survives restart. Callbacks use ObserveReply or
ResolveReplyDelivery, not a business request fabricated from the receipt.

An echo arriving before its MessageID has been registered must be identified
by the adapter and correlated through ObserveReply first. If a platform cannot
recover a handle or authenticate its own echo, the host must buffer or review
that callback instead of submitting it as business input. This library does
not infer sender identity from text, provide platform authentication, or promise
exactly-once delivery on transports without an idempotency/query contract.

A MessageID cannot be bound to two deliveries in the same source/thread, or
bound after that identity has already been admitted there as business input.
Those errors leave the attempt unresolved; they do not undo already processed
business work. The adapter must investigate the conflicting identity.

## Recovery and factual boundaries

`Reply.Attempts` retains started, unknown, not_delivered and delivered facts.
Started and unknown remain `needs_review` on reopen. No send is automatically
replayed. The host can recover the exact unresolved handle from that history
and query its transport. Conflicting terminal results are rejected without
changing state. Identical terminal receipts are idempotent, including a delayed
duplicate from an earlier attempt; they cannot settle or reset a newer attempt.
If later evidence contradicts a claimed non-delivery, stop and investigate it,
rather than force a stale callback into a newer attempt.

Validation runs before append and on replay. A failed append does not publish
a send handle or apply a result and retains the journal's existing fail-stop
behavior. Inference, Worker runs, and external send calls do not run under the
delivery lock. Pending or unknown replies do not block other work or WaitIdle.

Delivery does not edit the Report, tool receipts, Worker snapshot or task review
barrier. In particular, an acknowledged reply can still carry partial failure,
Committed effects and NeedsReview. Hosts must render these facts alongside
generated prose. Existing main snapshots retain prior report inputs for model
summaries; structured multi-report citations and additional report-selection
APIs are not introduced by this delivery slice.

## Draft API and journal migration

`BeginReplyDelivery` changes from `error` to `(ReplyDelivery, error)`. Migrate
external callers to retain its handle and call ResolveReplyDelivery or
ObserveReply; do not call AcknowledgeReply after sending. No transport-side
action occurs inside these methods.

The new reader accepts M1a/M1b `reply_started` as a first unresolved attempt and
preserves old `reply_acknowledged` records. An unresolved legacy send can be
verified with its recovered handle. Already acknowledged legacy records have
no platform ID, which cannot be retroactively invented or filled by this API;
adapters must filter their old echoes themselves. Unsent old replies still use
the direct memory-reader path. New writers emit reply_claimed, reply_resolved
and reply_read. Older binaries cannot replay those records: stop the single
writer and back up the journal before upgrading; do not downgrade the writer.

Known-thread business routing remains the M1b behavior. Same-chat topic
classification, multi-target delivery, real channel adapters, scheduling
external retries and report retrieval are separate work. No merge, deployment,
live provider request, external message send or manual CI dispatch is needed to
exercise this slice.

## Verification

The acceptance test was committed and failed against the verified #158 HEAD
`61aec9be8be94e030773b64a17e91e363e535a39` before implementation. Tests cover
uncertain-send reopen, verified non-delivery retry, stale callbacks, concurrent
claimants, invalid/conflicting receipts, exact echo filtering before and after
restart, every delivery commit boundary, append failure, legacy upgrade,
platform identity collisions, report isolation and independent work progress.

Run in the remote Linux environment with official Go 1.26.10 and offline fixtures:

```powershell
go test -mod=readonly -count=1 ./...
go vet -mod=readonly ./...
go test -mod=readonly -race -count=1 ./superagent ./agent/session
go test -mod=readonly -race -count=20 ./superagent -run 'TestReplyDelivery|ExampleSuperAgent_BeginReplyDelivery'
# Also run readonly tests and vet from the examples module.
```

Linux amd64 is the verified platform; Windows/macOS and real platform protocol
integration have not been exercised. Prefix replay simulates loss at durable
boundaries; it is not a claim of end-to-end process-kill testing of an external
messaging service.
