# M2b: frozen Thread deliveries

## Scope and acceptance contract

This isolated slice starts from draft #163, remote commit
`e78a18a1752fd8fc551b82ad09c82525d4d75dcb`, tree
`8f48f54f120926ebf791084324ab1b5fc19138a0`. Its requirements come from
[M2b in the development plan](superagent-development-plan.md). Dependencies
are not merged or deployed.

Acceptance criteria, written before implementation:

1. A trusted host subscribes tasks A and B to one `(source, thread)`.
   `SubmitThread` writes the external event once and fixes its ordered target
   list in that admission. B unsubscribes and C joins. After restart, the old
   event still belongs only to A/B; the next event belongs only to A/C.
2. Each target has an independent serial execution, terminal report, snapshot,
   receipts and review barrier. A success and B interruption remain distinct.
   Exact-run controls can address B, not accidentally A. Duplicate ingress,
   including after subscription changes/restart, cannot execute again.
3. Ordinary `Submit` remains a chat input; `SubmitTask` remains one trusted
   explicit association. The new entry point never silently changes either.
   A Thread with no subscribers is fixed as a main-session input at admission,
   not delivered to subscribers added later.
4. Host subscription changes are durable and idempotent. Worker callbacks can
   change only their own task's subscription to the current authorized Thread,
   are bound to one active invocation, and have a bounded mutation budget.
5. Worker handback retains the original event, exact target, bounded handled
   scope and reason. It is recorded once per delivery, survives restart, and
   leaves the worker's terminal effects and receipts intact. This conservative
   slice asks main to clarify from the first handback; it never automatically
   sends the original event back to workers, so repeated handback cannot loop.
6. Old journals retain single-target task and provider identities. Legacy
   pending inputs keep their old routing rules. Malformed new admissions fail
   before changing in-memory state; disk replay rechecks all invariants.

Multi-hop automatic handback rerouting, model-facing worker tools and CLI
subscription commands are outside this slice. M2c still owns the broader
report-consumer migration. No real provider, external service, user computer,
Codex task, paid runner, manual CI, merge or deployment is part of validation.

## Implementation and verification

`SetThreadSubscription` changes the trusted host's future subscription relation;
`ThreadSubscriptions` returns a detached ordered view. `SubmitThread` commits one
`thread_received` record with the full target list, then creates independent
deliveries with the original event and each existing task's identity. Their
normal task queues retain order without blocking unrelated tasks. Duplicate
admission checks the original event and entry point, not the current relation.
Subscriptions have limits of 128 tasks per Thread and 128 Threads per task.

The task-specific callbacks on `Job` can change only that task's relation to its
current source/Thread, while the exact Worker invocation is active. They do not
accept arbitrary task or Thread IDs. Subscription changes have a durable budget
of 16 actual mutations per invocation; identical requests consume no budget.
The host API remains separately authorized. The callbacks are not automatically
exposed as model tools.

`Job.HandBack` records one immutable original event, exact `RunTarget`, reason
and handled scope. Identical retries are idempotent. The main input and resulting
reply carry detached `Handback` facts. The coordinator accepts only `clarify`
for this input, preventing automatic rerouting even if the model asks for it.
Handback neither stops the Worker nor erases its terminal report or receipts.
Its public text must omit private data, just like other Worker summaries.

Exact controls and `DeliveryFailureCause` resolve by task/source/event, including
the second recipient of a fanout. The older `FailureCause` joins task-labeled
in-process errors. Terminal reports remain separate and `ReportsSince` remains
a count cursor; consumers must not deduplicate them by source/event alone.
The complete report-consumer/CLI migration and explicit delivery IDs are M2c.

Old legacy bindings are imported when first established. Old pending inputs
retain their original task and Provider session, but cannot recreate a future
subscription explicitly withdrawn after upgrade. Independent review found that
resubscription bug; a deterministic regression failed before the fix and passes
after it, including disk replay. Malformed admissions and nested mixed-envelope
assignments are rejected before append/mutation and rejected again on replay.
New `thread_*` records require this version; do not downgrade an upgraded journal.
The final independent review confirmed the fix and found no remaining blocker.

Offline verification uses official Go 1.26.10 on Linux amd64:

```powershell
go test -mod=readonly ./...
go vet -mod=readonly ./...
go test -mod=readonly -race ./...
go test -mod=readonly -race ./superagent -run 'Thread|SameChat|Correction|MainChatUpgrade|LegacyUpgrade|ExampleSuperAgent_SubmitThread' -count=20
go test -mod=readonly -race ./agent/toolset -run 'Scope|Permission|CatalogGuard' -count=10
# In examples/:
go test -mod=readonly ./...
go vet -mod=readonly ./...
```

Coverage includes frozen A/B to A/C recipient churn and restart, empty admissions,
per-task serialization while another task is blocked, second-recipient steering
and cancellation, independent committed receipts/review barriers, stale callbacks,
worker mutation budgets, append failure, malformed on-disk records, every fanout
journal boundary, and a torn admission that exposes no partial recipient list.
`ExampleSuperAgent_SubmitThread` is an executable public host example.

No dependency or workflow configuration changes are needed. CI remains manual
and was not dispatched. Windows/macOS, live providers/platforms, automatic
handback rerouting and production performance remain unverified.
