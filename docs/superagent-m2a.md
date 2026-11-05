# SuperAgent M2a: separate chats from tasks

2026-11-05. Development slice based on draft #159 at
`859e0f598e69efa787f26cf76d50d72991f1c67b`; dependencies remain unmerged.
This is an offline-tested library change, not a deployed service or release.

## Entry points

`OpenMain` now treats every ordinary `Submit(trigger)` as a main-chat input,
except deterministic noise and confirmed own-message echoes. An earlier task in
the same `(source, thread)` no longer captures every later message. The main can
start independent work, attach a follow-up, ask about ambiguous ownership, or
select an exact offered `RunTarget` for steering/cancellation. Worker limits and
per-task serialization are unchanged. A task's review barrier does not block an
independent request in the same chat.

`SubmitTask(trigger, taskID)` is for a trusted host's explicit association with
an existing task: for example, an authenticated review event for that task's PR.
It requires `OpenMain`, rejects noise/unknown tasks, and schedules an ordinary
serial task input without waiting for main inference. It does **not** steer the
active invocation. Use a normal chat input and a validated `steer` decision for
a durable correction, or native `Steer/Abort` for host-controlled exact runs.
Do not derive authority from task IDs embedded in untrusted text or links.

The main also receives a `task_input` observation containing the event identity,
task identity, and at most 1,024 UTF-8 bytes of event text. This is a summary of
an already admitted update, not a second dispatch opportunity. It allows only
`reply`, `clarify`, or `ignore`; even a request-kind observation cannot start,
attach, steer, or cancel work. Worker completion later returns through the
existing `worker_result` path. Private Worker history is not copied to main.

An update is durably admitted once by `(source, event_id)`. The explicit task
association is part of duplicate-content validation. Repeating the event with
another task, or switching between `Submit` and `SubmitTask`, is a conflict,
including after restart. Repeating the identical call returns `false, nil`.
The admission boolean is never a delivery or execution acknowledgement.

See `ExampleSuperAgent_SubmitTask` for an external-package Go host example.
The legacy `Open` API and CLI retain their existing stateless/single-thread
subscription behavior; `SubmitTask` is deliberately not added to that mode.

## Durable correction evidence

Existing main `steer` decisions retain their original input ID, source/event,
stable control ID, and complete target task/source/event. They cannot silently
retarget a later invocation. Native `Steer` remains transient and exact-run.

- `pending`: the main decision was committed but not issued.
- `queued`: the correction is in the target run's mailbox, not delivered.
- `delivered`: the Worker dequeued it at a safe input boundary. This alone does
  not prove the Runner incorporated it or that the model obeyed it.
- `InputAccepted=true`: the target invocation's saved user-message history
  contains the exact coordinator-owned correction identity, origin, run and
  text. This still does not establish model compliance or successful effects.
- `not_delivered`: it was never handed to the Worker, including unpolled exit,
  cancellation, full mailbox, or restart before dequeue.
- `needs_review`: dequeue was recorded but accepted input cannot be established.
  Missing metadata is uncertainty, never permission to inject the instruction
  again. This includes a nominally successful custom Worker omitting history.

PiWorker already preserves accepted partial input on turn failure. Corrections
are now tagged in message metadata and checked against the exact terminal
snapshot. Later task snapshots or compaction cannot erase established evidence:
replay derives it from that original terminal record. A crash after the dequeue
record but before a saved terminal snapshot remains uncertain. Neither positive
input evidence nor a reply acknowledgement clears the task's independent
interruption/effects review barrier.

Each new `not_delivered`/`needs_review` outcome creates one durable-derived
`control_result` in the main inbox, linked to the original correction and chat.
Main can ask for verification, reply, or ignore; this observation cannot
authorize a fresh dispatch or another control. It is reconstructed exactly once
on replay. No correction is automatically re-injected into the next run.
Replies to these observations carry authoritative `Reply.Control` facts beside
model-generated text, so a host can surface uncertainty even if wording is poor.

Custom Workers are trusted host code. They must return the accepted user
messages unchanged, including coordinator metadata, to establish input
acceptance. This is bookkeeping evidence within that trust boundary, not
cryptographic attestation or proof that an arbitrary Worker is honest.

## Migration and cost

`main_chat_opened` is a durable upgrade boundary. Earlier records replay with
their original subscription rules and inbox ordering. Pre-upgrade admissions
already bound to a task retain that task even if dispatch happens after upgrade.
Subsequent ordinary chat admissions use the new rules; old task/provider/main,
decision, reply, and input identities remain intact. Earlier controls without
the new marker retain their conservative M1 delivery semantics. Older binaries
cannot read the new record; do not downgrade a journal after opening it here.

No hash was added. Admission uses the existing event-key map and direct envelope
comparison. Control-ID lookups now use an index, and correction checks reuse the
already decoded terminal snapshot, scanning it once only if that exact run has
delivered corrections. There is no repeated full-history scan, full-file hash,
extra snapshot decode, or dependency on the separate snapshot-optimization PR.

## Acceptance and verification

The first commit contains a failing regression against #159: while logs task A
is blocked, a tests request B in the same chat waits for A. The implementation
passes the complete conversation: B starts, finishes and replies before A is
released; a same-chat correction reaches only A; both tasks and replies retain
stable identities after restart and duplicate submissions.

Additional tests cover explicit cross-source input during blocked main
inference, observation authority restrictions, changed-association duplicates,
admission/recovery boundaries, append failure, review-barrier isolation, old
bound pending admissions, exact metadata mismatch, partial Pi failure, missing
snapshot uncertainty, one clarification after repeated reopen, and no steering
spillover after reconciliation or replacement of the latest task snapshot.

Validation uses official Go 1.26.10 on Linux amd64 with offline fixtures:

```powershell
go test -mod=readonly -count=1 ./...
go vet -mod=readonly ./...
go test -mod=readonly -race -count=1 ./superagent ./agent/session
go test -mod=readonly -race -count=20 ./superagent -run 'TestMain(SameChat|Task|ChatUpgrade|Correction|Informational)|TestPiMainCorrection|ExampleSuperAgent_SubmitTask'
# In examples/:
go test -mod=readonly -count=1 ./...
go vet -mod=readonly ./...
```

Independent contract/implementation review was performed. No real providers,
platform messages, user computer, extra billed task, manual CI dispatch, merge,
or deployment were used. Windows/macOS and live platform adapters remain
unverified. M2b many-to-many subscriptions/handback and M2c delivery identities,
as well as real semantic-routing evaluations, remain follow-on work.
