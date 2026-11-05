# Host-owned channel boundaries (M5a and M5b)

This optional package connects authenticated ingress and approved outbox records
to a host-supplied transport. The core `Adapter` does not create connections or
credentials, infer permission from messages, or run a send/retry loop. M5b adds
optional `GitHubWebhook` and `GitHubTransport` HTTP boundaries described below.
Tests use in-memory simulators and isolated `httptest` HTTP servers, never live
GitHub accounts or API requests. IM remains a simulator, not a real service.

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
the raw comment/review ID. For `edited` comments, sender is the current editor,
while comment.user.id identifies the original author and may differ. The author
must still be a valid positive decimal ID, but does not grant the editor any
authority. Created comments and submitted reviews require sender and author to
match. The host must authenticate and authorize the editor, target PR, intent
and any task/thread association; the payload cannot confer those permissions.
At this low-level API, EventID is a host-verified stable event ID, not the message
ID. HMAC alone does not authenticate the GitHub delivery or event headers. The
HTTP wrapper below therefore uses a raw-body digest rather than trusting a
delivery header as its durable event identity.
Numeric IDs remain exact through uint64; body must be a string, so null-body
review events are not generalized.
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

These are M5a/M5b development slices, not complete M5: live platform acceptance,
verified service permissions, durable transport-side idempotency/queries, richer formatting,
server-side pagination cursors, email/document/calendar adapters, shared-state
leases and remote execution remain later work. No exactly-once delivery claim is
made for a platform lacking authoritative evidence.


## Optional GitHub HTTP webhook (M5b)

`NewGitHubWebhook(adapter, config)` accepts a GitHub Adapter, existing-secret
provider, exact `repositoryID/pull/number` allowlist, and an independent host
`Authorize` callback. Empty targets and zero authorization deny. The callback
must deliberately authorize sender, intent and task/thread association; HMAC,
repository names, mentions, author_association and a comment's original author
never grant permission. The coordinator's existing actor policy remains a
separate final check. Edited comments are authorized as `sender.id`.

`Admit(*http.Request)` consumes the request body and returns a result/error; it
is not an HTTP handler and writes no response. The host owns TLS, a trusted
network boundary, header limits and request/body deadlines. Only POST JSON with
one signature, delivery and supported event header is accepted. Encoded bodies
are rejected. At most 1 MiB + one byte is read, then SHA-256 HMAC is compared in
constant time over the exact raw bytes. Secrets must be 32–65536 bytes and must
already exist; the wrapper never generates, discovers, persists or logs them.
The provider retains ownership of its bytes and must be concurrency-safe.

JSON normalization rejects duplicate keys, invalid Unicode and ambiguous event
shapes. All established M5a content/action/identity bounds still apply. The exact
repo/PR allowlist is copied at construction. The host gets a detached event and
raw-body copy only after validation; mutating the callback argument cannot alter
admission. Secret/authorization errors or panics fail closed with generic errors.
No webhook path calls SendReply or otherwise posts a comment.

**Unsigned headers and replay limitation:** GitHub's HMAC signs only body bytes.
Neither `X-GitHub-Delivery` nor `X-GitHub-Event` is covered, and there is no signed
freshness timestamp. The event header must match an unambiguous supported body
shape. A syntactically valid delivery UUID is retained as `Event.DeliveryID`,
not promoted into authenticated event identity. Admission uses
`github-body:<SHA-256 of original bytes>` in the existing source namespace. This
fence survives coordinator restart and prevents changing only delivery headers
from rerouting an already seen signed body.

This is deliberately conservative, not an exact event-identity proof:

- Repeated signed bytes return `ErrGitHubReplay`, the current raw evidence and
  the original admission when available. Known own echoes also return this error
  but may have no admission record. Hosts should separately acknowledge ordinary
  redelivery or investigate a claimed independent event; do not automatically
  retry admission under a random ID or silently treat it as a new request.
- Two legitimately distinct occurrences with byte-identical bodies cannot be
  distinguished here. The host must retain each returned header/body occurrence
  in its own protected audit and use independent platform evidence before deciding
  what to do. The wrapper does not persist raw headers/bodies; the coordinator
  retains normalized text/identities and its frozen admission result only.
- Identical text with different signed comment IDs or other body bytes remains
  distinct. The body digest is not a text-only dedupe rule. Later host grant
  changes do not replace the original accepted body's routing decision.
- An old valid signed body never seen by this journal can pass HMAC. If freshness
  is required, the host must verify it separately. No security claim is made
  against a compromised signing secret or untrusted host callback.
- An early own echo without a recorded delivery needs independent host correlation
  and `Observe` **before** admission. The wrapper does not infer authorship or
  attempt identity from matching text. Uncorrelated bot events should not receive
  a request grant.

The host must choose HTTP response/retry policy and protect retained raw audit
content. Never expose raw webhook evidence or internal errors publicly. If the
host needs strict platform delivery IDs, replay windows, delivery logs or a
multi-process inbox, it must supply that evidence/storage before production use.

## Optional GitHub HTTP egress (M5b)

`NewGitHubTransport` takes one explicit API base URL, `Source`/owner account ID,
an exact thread-to-owner/repository mapping and an injected `*http.Client` with
an explicit RoundTripper. The RoundTripper supplies existing credentials and
must authenticate the endpoint without automatic POST retries. No environment,
CLI token, cookie jar, key generation, OAuth flow or secret storage is used.
Configuration and client values are copied; the host owns RoundTripper lifetime
and concurrency. The host must verify ID-to-name mappings, including after
repository transfers/renames. Payload repository names cannot select endpoints.

Only HTTPS origins (optional `/api/v3` for Enterprise) are accepted, except
numeric-loopback HTTP for local tests. Safe fixed owner/repository path segments
and exact allowlisted PR numbers construct the issue-comment POST. Redirects are
never followed, cookies are not imported, response bytes are capped at 1 MiB and
client timeout is capped at 30 seconds. Existing stricter host timeout is kept.
Request text is the exact Router-approved `{body}` from `Adapter.SendReply`.
The transport itself is trusted low-level infrastructure, not an approval API.

A delivered receipt requires HTTP 201, strict JSON, a canonical positive comment
ID, exact returned body, and matching API issue/comment URLs. Other status codes,
wrong destinations, malformed/truncated responses, transport errors and lost
receipts remain unknown. Private remote body/error text is not journaled.
HTTP 403 with authenticated rate hints or HTTP 429 with valid hints can record
rate rejection; the later of Retry-After and reset is honored with a one-second
safety margin. Invalid, duplicate, missing or past hints remain unknown, not a
made-up retry deadline. In those cases the host should suspend further source
traffic pending independent verification; this slice does not infer a source-wide
rate deadline from ambiguous responses. Valid deadlines use M5a's persistent
source-wide gate, including across restart. In-flight requests cannot be recalled.

GitHub issue-comment creation has no idempotency guarantee here. The internal
reply ID is not sent as an idempotency header, no POST is automatically retried,
and `Lookup` never scans comments or resends: it returns unknown without network
traffic. A body match or missing listing result cannot prove delivery/absence.
Only independently authenticated host evidence may resolve an unknown attempt.
No formal review, approval, merge, inline review comment, polling, pagination,
installation/token management or real account connection is provided.
