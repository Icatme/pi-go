# CLI main-session migration

This slice follows M2c on v0.20.0 / master
`7a908246e28b01a0b18b39ab06af7789c985e3e7`. It reuses the existing
`OpenMain`, `PiMainRouter`, Worker scheduler, controls and reply journal. No
new coordinator or storage adapter is introduced. The root module remains
free of PostgreSQL drivers; non-bundled persistence remains host-owned through
`Journal` / `LeasedJournal`.

## Input and task boundaries

`run` and `demo` now open a persistent main session. The input event schema
and `(source, id)` ingress deduplication are unchanged. Each ordinary chat
input reaches the main router; a shared source/thread alone no longer attaches
it to the previous task. The deterministic offline fixture starts a new task
for every request and attaches a signal only when its first word explicitly
names an offered `@task-ID`. The demo uses two tasks in one chat and explicit
follow-ups. Real providers use the existing structured main-decision contract.

Exact `steer` and `abort` controls retain `(task_id, source, event_id)` targets.
Their existing admission/receipt semantics are unchanged. A reply ID is not
a control target. Progress `subscribe` / `unsubscribe` remains lossy and
process-local, distinct from durable Thread routing subscriptions. The CLI
does not add `SubmitThread` or Thread-subscription commands.

## Output and consumption

- `REPORT`: original terminal facts and delivery ID. Existing report-count
  replay, fanout identities and deduplication remain unchanged.
- `REPLY`: the complete public `Reply`, including stable `id`, host-owned
  destination `source`/`thread`, input/delivery/task correlations, status,
  authoritative triggering `report`, structured `reports` citations, and any
  control/handback/transport-attempt facts. Private Worker snapshots and raw
  provider failures are not serialized.
- `MAIN_ERROR`: safe persisted `MainFailure` text and `input_id`. A failed
  result synthesis cannot silently disappear behind a successful Worker report.
- `REPLY_ACKNOWLEDGED`: `ack_reply` control response with `reply_id`,
  `input_line` and optional `request_id`. This records a direct consumer read,
  not platform delivery. Unknown IDs, extra delivery fields and attempted
  external sends are rejected without changing their facts.

Copy the actual REPLY ID into `{"op":"ack_reply","reply_id":"..."}` only
after direct consumption. Repeating it is idempotent, including after losing
the response or restarting. Successful stdout writes never acknowledge replies
or create delivery attempts. For external sends, use the existing Go claim,
resolve and observe protocol with trustworthy platform evidence. The CLI has
no external transport and cannot certify delivery.

Consumers namespace IDs by the logical journal. Deduplicate REPORT by
`delivery_id` and REPLY by `id`; they are separate identities. On each open,
all reports, replies (including acknowledged ones) and public main failures
replay from zero. REPLY status is an observation when staged; later
acknowledgements are separate control responses. Do not treat replayed pending
frames as fresh permission to send or override a later consumption receipt.
Different frame types are snapshots, not one globally ordered journal cursor;
correlate their IDs and attached facts rather than assuming cross-type ordering.

Direct output cursors advance only after the entire batch succeeds. In
streaming mode they acknowledge staging, not downstream consumption. A partial
write may replay a prefix with identical IDs. The existing bounded memory queue
spills to private OS-temp files so stalled stdout does not block controls;
files are removed after draining or an output error. The durable journal,
not the spool, is the recovery source. Per-stream staged cursors are not saved.

## Upgrade and limits

Stop the previous writer before upgrading. Legacy completed reports retain
their delivery IDs and do not generate new main replies or Worker execution.
Existing task and Provider snapshots are unchanged. Opening this CLI upgrades
the journal to the existing no-downgrade `OpenMain` boundary.

`-main-context-bytes` bounds the existing main model context (1 KiB–8 MiB,
default 1 MiB). Overflow becomes an explicit persisted main failure, not
silent history truncation or automatic retry. Raising the limit does not
resubmit a previously failed input. Real models/credentials remain opt-in via
the existing provider/model flags. No real model or transport was used here.

## Validation

Acceptance tests were run before implementation: legacy CLI produced no REPLY
for two same-chat requests and the partial-reply-output test failed because
there was no reply output. Tests then cover stable restart IDs and destinations,
independent same-chat tasks, exact explicit follow-up, idempotent direct
consumption, refusing acknowledgement of uncertain external attempts,
authoritative report references and committed-effect/review facts, private
error/transcript isolation, partial output replay, legacy upgrade, main-context
failure visibility, exact steer/abort and blocked-stdout control.

Validation uses the cached official Go 1.26.10 toolchain on remote Linux amd64,
readonly modules, offline module lookup and disabled live-provider flags.
PowerShell is absent in that environment; existing bash is used without
installing a shell. Final command results and independent review are recorded
in the worklog. Windows/macOS, real channels, external delivery and production
load are not certified by these offline checks. No manual CI, merge or deployment.
