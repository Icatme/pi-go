# M2c: delivery identities and report consumers

## Scope and acceptance contract

This development slice depends on draft #164, exact remote commit
`e50211e758c0ecd696ec76b28c3ecadac4bbe6e0`, tested tree
`17cbf2be69d21d3e5d4517268bb8da9dbe12e14c`. Dependencies are not merged
or deployed. The [development plan](superagent-development-plan.md) defines
M2c as the report-consumer migration after frozen multi-target admission.

Acceptance cases were written and run before implementation. M2b failed them
because fanout, old-journal and unassigned reports had no `delivery_id`; the
CLI also advanced its in-process cursor after an output failure. The slice
must preserve both fanout reports, preserve their IDs on replay, keep each
target's effects and review state, read old journals without new work or
changed task/provider identities, and retain the existing report-count API.

## Identity and cursor contract

- **Ingress:** `(Trigger.Source, Trigger.ID)` remains the journal's external
  event deduplication key. `Submit`, `SubmitTask` and `SubmitThread` retain
  their existing admission and duplicate-checking semantics.
- **Task:** `TaskID` identifies a continuing Worker conversation. Its existing
  snapshot and `ProviderSessionID` are unchanged. A task can process many
  deliveries, and an event can be delivered to several tasks.
- **Delivery:** `DeliveryID` is an opaque, nonempty identity for one immutable
  admission slot, including ignored events or routing failures with no task.
  Each frozen fanout recipient gets a different ID. The same ID follows its
  `Job`, terminal `Report`, progress observations and active/review status.
  Main inputs and replies expose the originating delivery ID as correlation.
  For `control_result`, it identifies the correction's input, not the separate
  invocation in `Control.Target`.
- **Output:** `Reply.ID` is still the outbound message idempotency and
  acknowledgment key. Multiple main observations/replies can refer to one
  delivery. Do not deduplicate replies by `DeliveryID`. `RunTarget` remains
  the exact `(task_id, source, event_id)` control target; a delivery ID grants
  no authority and is not accepted as a replacement control target.
- **Consumer:** `ReportsSince(count)` is unchanged: zero replays all terminal
  reports in journal completion order; advance by the number of reports
  handled, not inputs. Clamp behavior for negative/oversized counts is retained.
  Delivery IDs are not cursor offsets and need not be in completion order.

IDs are unique **within one logical journal**, not globally. Consumers combine
them with a stable host-owned journal namespace. Keep that namespace when
moving/reopening the same journal. Use a new namespace for an unrelated or
rewritten history; an old checkpoint must not be applied to such a replacement.
Treat IDs as opaque. The implementation derives them from durable admission
slots, whose fanout order was fixed before any execution. It does not allocate
fresh IDs during replay or derive them from completion order. No additional
log records, on-disk fields or journal rewrite is needed. Future compaction or
import must preserve these slots or explicitly persist their identities.

## Upgrading a host

1. Stop the old writer and consumer before upgrading. Retain the exclusive
   single-writer lock and the same authorization-domain boundary. Existing
   legacy bindings, reports, task IDs, provider identities, snapshots and
   review barriers replay without conversion writes. Old completed reports
   are not new main-session work. Once upgraded to `OpenMain`, its existing
   no-downgrade restriction still applies.
2. Read the new `Report.DeliveryID` / JSON `delivery_id` and deduplicate using
   `(journal namespace, delivery_id)`. Go function signatures and `RunTarget`
   are unchanged. New exported fields require callers with positional struct
   literals to use named fields; strict JSON decoders must accept the additive
   fields. These identities are observations, not additional routing inputs.
3. Keep an existing **report-count** checkpoint if it belongs to this exact
   journal. If rebuilding the dedup store, replay the already acknowledged
   prefix and seed its delivery keys before resuming. A historical
   `(source,event_id)` acknowledgment can be mapped only when it is known to
   represent a single-target report. If an M2b consumer already collapsed a
   fanout, that key cannot prove which recipient was handled; reconcile the
   sink before advancing either target. Do not guess or resend uncertain
   external effects.
4. Commit a report's sink effect and dedup key atomically, or use an equivalent
   idempotent sink protocol. Save the new count only after processing. Replaying
   a lost checkpoint returns the same delivery IDs. This is not an exactly-once
   external transport guarantee. For user-visible sends, use the separate
   reply attempt/confirmation protocol and retain uncertain outcomes for review.

`Subscribe(ctx, taskID)` is a bounded, lossy, process-local progress observer;
it never changes routing. `SetThreadSubscription` changes durable authorization
to receive future Thread events. Canceling a progress observer cannot unsubscribe
a task from a Thread. A progress `DeliveryID` is correlation, not completion.

## CLI and executable examples

The original M2c slice intentionally retained legacy `Open` routing. The
[subsequent CLI migration](superagent-cli-main.md) replaces it with `OpenMain`
and adds explicit main-reply consumption. Its `REPORT` frames
carry `delivery_id`, including on restart, and help text gives the new dedup
contract. `ACCEPTED`/`DUPLICATE` remain ingress acknowledgments. Failed direct
report output no longer advances the cursor; a partial write may replay a
prefix with the same IDs. Streaming mode advances only after staging a batch,
not external acknowledgment; restart still replays from zero.

`ExampleSuperAgent_ReportsSince`, in the external `superagent_test` package,
demonstrates an offline public Go host, a two-target event, report-count
consumption and replay into a journal-namespaced idempotent sink. Its in-memory
map is instructional, not a durable production sink. Existing legacy Open and
Thread examples now expose or check delivery identity too. CLI serialization
tests use a real `OpenMain` fanout to ensure the shared output path never
collapses targets, without silently changing the original slice's ingress behavior.

## Verification and boundaries

Offline validation uses the existing official Go 1.26.10 toolchain on remote
Linux amd64, readonly modules and disabled live-provider flags:

```powershell
go test -mod=readonly -count=1 ./...
go vet -mod=readonly ./...
go test -mod=readonly -race -count=1 ./...
go test -mod=readonly -race ./superagent -run 'DeliveryIdentity|Thread|SameChat|Correction|LegacyUpgrade|ExampleSuperAgent_ReportsSince' -count=20
go test -mod=readonly -race ./cmd/pi-superagent -run 'OutputSession|Control|Output' -count=20
go test -mod=readonly -race ./agent/toolset -run 'Scope|Permission|CatalogGuard' -count=10
# In examples/:
go test -mod=readonly -count=1 ./...
go vet -mod=readonly ./...
```

Coverage includes journal replay at every fanout commit boundary; legacy
single-target logs and original provider fallback; no-task reports; independent
success/failure with committed receipts; exact running/review progress identity;
unchanged ingress dedup and count cursors; output failure/replay; and external
public-API consumer execution. Existing tests cover authorization isolation,
second-recipient steering/cancellation, handback, uncertain reply sends, torn
records and recovery barriers.

All commands above passed. Independent review found no blocking defect; its
suggested assertions for reply presence and main/progress identity were added
and re-reviewed. Partial-output retries are also checked after one report has
already been emitted, preserving the same IDs for the repeated prefix.

No dependency/workflow changes, user-machine access, Codex execution, additional
billed service, live provider, manual CI dispatch, merge or deployment is part
of this slice. Windows/macOS, real transports and production throughput are not
validated here. CLI Thread-subscription commands, persistent remote sinks and
cross-journal import/compaction remain separate work.
