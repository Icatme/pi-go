# Host-owned channel boundaries (M5a)

This optional package connects authenticated ingress and approved outbox records
to a host-supplied transport. It does **not** connect to a real IM service or GitHub,
make network requests, handle credentials, validate webhook signatures, infer
permission from messages, or run a send/retry loop. Its executable example and
contract tests use in-memory JSON protocol simulators only.

## Ingress

1. The host authenticates a callback using the platform's supported mechanism,
   binds it to one authorized account/organization, principal and conversation,
   and resolves any approved task association independently of message content.
2. Configure `Protocol`, `Organization`, and `Source` exactly as
   `string(Protocol)+":"+Organization`. That stable namespace binds persisted
   replies to the same platform/account after a restart. Use one coordinator
   journal per host authorization domain. Rules must match that Source and use
   Organization as their Group. Configuration is copied by `New`.
3. Pass independent verified `Identity` facts and bounded bytes to `Adapter.Admit`.
   `Normalize` cross-checks the wire identities; `SuperAgent.Admit` owns the durable
   policy result, `(Source, EventID)` dedupe and frozen task/thread destinations.
   Normalize alone is decoding, not authentication. It returns no authority.
4. `ExplicitRequest`, `TaskID`, and `ToThread` are trusted host decisions. Body
   text, mentions and PR links never grant them. Cross-source follow-ups require
   an explicit host mapping to the original task or its subscribed Thread.

IM is an intentionally generic **simulator** schema with exactly five string
fields: `organization_id`, `actor_id`, `thread_id`, `message_id`, and `text`.
EventID equals message_id; optional MessageID must also match. This is not a
claim of Slack, Teams, Mattermost or another platform's protocol compatibility.

GitHub uses a bounded webhook JSON subset and a host-authenticated `EventType`:

- `issue_comment`: `created` or `edited`, only with `issue.pull_request` object.
- `pull_request_review`: `submitted`.
- `pull_request_review_comment`: `created` or `edited`.

Identity.Organization is repository.owner.id (personal or organization account),
Principal is sender.id, Thread is `repository.id/pull/number`, and MessageID is
the raw comment/review ID. Sender must match the content's user.id; an edit by a
different actor is outside this slice. EventID is the stable authenticated
webhook delivery ID, not the message ID. Numeric IDs remain exact through uint64;
body must be a string, so null-body review events are not generalized.
GitHub's extra payload fields are tolerated but cannot change authority.

The normalized envelope persists the platform object kind plus ID, e.g.
`issue_comment:901` versus `pull_request_review:901`. Those object namespaces can
have the same numeric ID. The coordinator checks known echoes under its admission
lock, not against a prior adapter snapshot. Receipt confirmation after a matching
message was already admitted is rejected instead of concealing prior business
input. The host must correlate and Observe an early own echo **before** admitting
it; missing or unauthenticated correlation is not proof of a new user request.

Payloads are limited to 1 MiB, text to 64 KiB, identities to 256 UTF-8 bytes and
JSON nesting to 64 levels. Duplicate keys, trailing JSON, invalid UTF-8/surrogates,
noncanonical numeric IDs, mismatched identities and unsupported actions fail
closed before admission. Credentials and raw webhook headers are never persisted.

## Approved egress and transport evidence

`SendReply(ctx, replyID)` accepts only an existing outbox ID. It reloads the
immutable Router-approved text and destination, encodes that text without a
summary or appended Worker content, and calls `BeginReplyDelivery` before Send.
IM encodes `{thread_id,text}`. GitHub encodes `{body}` to create a PR discussion
issue comment, **not** an inline comment, formal review, approval or merge.
The Router chooses appropriate reply length; this package does not rewrite it.

Transport gets the exact Source/Organization/Thread, claimed delivery attempt,
stable reply ID as IdempotencyKey, and detached JSON body. A stable key does not
claim that a particular platform supports idempotent creation. The host owns
endpoint/permission checks, API version, escaping, request bounds and actual
platform limits. No caller-selected arbitrary body or destination is accepted.

Transport receipts must bind all destination fields and the exact attempt:

- `delivered`: authenticated platform message ID; optional opaque receipt Cursor.
  GitHub returns a raw positive decimal issue-comment ID; the coordinator stores
  its `issue_comment:` namespace. Cursor is a receipt fact, not a global ordering
  watermark. Identical delivery can fill a missing cursor, never replace it.
- `not_delivered`: authoritative proof the attempt ended without a message and
  cannot still create one. This makes the reply pending again.
- `rate_limited`: that same no-delivery guarantee plus absolute Unix-millisecond
  RetryAt. The deadline persists in the journal. All **new claims** in the Source
  conservatively wait until it expires, including after process replacement.
  Already in-flight sends cannot be recalled. Other source namespaces continue.
- `unknown`: preserve needs_review. Timeout, error, panic, absent lookup results,
  malformed/mismatched response, and non-authoritative HTTP failures are unknown,
  never proof of non-delivery. Private transport error text is not journaled.

`Reconcile` only calls read-only Lookup for the current unresolved attempt. It
never sends, schedules, or retries. After authoritative non-delivery and any
persisted deadline, the host may explicitly call SendReply again with the same
reply ID; the attempt number changes and old callbacks cannot reset it. Replacing
a simulator/transport and losing its registry does not prove absence of effects.
A successful send whose local receipt commit fails remains fenced by its durable
claim and requires the same original-identity lookup after recovery.

`Observe` accepts only host-authenticated callbacks. It does not parse untrusted
callback JSON or grant authentication. Destination mismatch is rejected without
changing state. Early observation, late return, duplicate receipts and concurrent
claims preserve terminal facts. There is no notification or proactive-send grant.

## Persistence and limits

M1c's existing reply state machine remains the single owner of delivery state.
New optional MessageID, Cursor, and RetryAt facts use new channel-specific journal
transition names, so old binaries reject these journals rather than silently
ignoring the identity or rate gate. New binaries continue reading old records;
legacy absent MessageID falls back to Trigger.ID. Back up before upgrading, keep
single-writer ownership, and do not downgrade or concurrently open the journal.

This is M5a, not complete M5: actual platform integrations, verified service
permissions, durable transport-side idempotency/queries, richer formatting,
server-side pagination cursors, email/document/calendar adapters, shared-state
leases and remote execution remain later work. No exactly-once delivery claim is
made for a platform lacking authoritative evidence.
