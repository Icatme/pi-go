# Session-backed direct-call receipts (opt-in)

This extends the in-process [receipt wrappers](../../executionreceipt/README.md)
for **one direct tool call**, including its children/Codemode host calls. It does
not integrate receipts into model-loop transcripts, `RunBinding.Run`, or
checkpoint recovery. Those paths still reject receipt capture. Existing
filesystem write tools have not been migrated; the #144/write-stack merge
blockers are not resolved by this increment.

## Host flow

1. Call `Session.PrepareReceiptCall(lane, RuntimeOptions{Scope: ...})` using the
   host's authenticated identity and current authorization epoch.
2. Use the returned single-use binding's `RunToolCall(ctx, call, options, limit)`.
   Limits are 1–128 captured bodies, including container/host/leaf entries. Its
   three return values are the ordinary tool outcome, an in-process handle and
   a **separate capture/storage error**. Never discard either error or the handle.
3. If cancellation returned with late bodies still running, retain this same
   binding and handle. `Finalize(freshContext)` waits for the original bodies,
   then conditionally saves their facts. It never reruns the tool and does not
   accept caller-supplied facts. Canceling only that wait permits another wait;
   a scope/storage failure invalidates the binding instead.
4. `ReadReceiptStatus` gives a fresh, scope-checked view of the last direct call.
   `Pending` is an unfinished intent, not proof the tool ran or did not run.
   A final status says all captured bodies exited, not that the operation
   succeeded. `Committed=false` means unknown, never no effect or safe retry.
   Facts and status are opaque immutable views, not a public JSON wire format.

This adapter adds no business messages, results, prompts or summaries to the
conversation. It does not restore tool state into direct executors. Host-owned
permissions remain required; receipt capture is not authorization. The binding
wraps the existing permission hook with scope checks before and after it, and
checks scope around storage acknowledgment. This is **not atomic revocation**
with a tool syscall or storage write. A changed epoch cannot take over an old
intent or complete it under a different identity. Even if a batch was written
before revocation was observed, it is not undone and the binding fails closed.

## Storage protocol and legacy compatibility

Before any tool body is entered, one `CompareAppend` atomically writes:

- An unchanged `agent.runtime.v1` record with `Pending:true`.
- A new `agent.direct_receipts.v1` **intent** record.

Only after acknowledgment may execution begin. When the in-process registration
has closed and every actual body has exited, a second `CompareAppend` writes
another legacy `Pending:true` record and a **final** receipt. The final contains
bounded identity/parent/effect/completed facts, not arguments, content, raw
outputs or errors. The old runtime fence is never cleared.

Pairing binds session, original record lane, scope/epoch, expected branch
version/head, fence entry ID, intent entry ID and a unique call token. Readers
reject unmatched or duplicate phases, invalid facts, changed scope, oversize or
unsupported payloads, and later unpaired legacy runtime records. Adjacency is
validated, but the guarantee of atomic batches depends on the Storage contract,
not on a reader proving how an arbitrary storage implementation wrote its data.

Old public structs, old runtime payloads, session headers and JSONL batch formats
are unchanged. The **actual baseline old binary** has been tested opening a file
written by this implementation, browsing both custom receipt payloads and
returning `ErrInterrupted` from old `PrepareRun`. New code can read old valid
sessions. Read compatibility is not permission for unaware runners to continue.
This phase does not change checkpoint formats or claim old checkpoint readers
understand receipts.

## Failures, forks and limits

- Intent write/acknowledgment failure means the tool is not entered. The binding
  is invalidated: storage may still contain that intent. Do not blindly retry.
- Final write/acknowledgment failure preserves the original memory handle and
  invalidates the binding. Reopen and inspect storage. A final may be present,
  or only the pending intent may remain; neither the error nor last memory view
  proves which batch reached storage.
- The external side effect and session append are **not one transaction**.
  A crash between them leaves an unresolved intent. After restart, pending
  histories refuse new receipt calls. There is intentionally no API to assert
  “nothing happened”, clear pending, replay, or reconcile a crashed call. Host
  investigation and a separately reviewed recovery procedure are required.
- Pending intents do not contain partial live commit facts. Those remain in the
  original handle until finalization. Losing the process can lose these facts;
  the persistent pending fence is the conservative recovery signal.
- Completed ancestors can be inherited by a fork; new calls append to that fork.
  Pending ancestors remain blocked. Branch movement or concurrent changes cause
  conditional-write conflicts rather than automatic rebase. Compaction retains
  ancestry for validation. Deliberately moving a lane before its intent, editing
  logs, or using raw host storage APIs to remove a fence is outside this safety
  boundary; this is not protection against a malicious host administrator.
- JSONL's existing torn-tail recovery may discard an unterminated partial final
  batch. The surviving intent must still block both readers' execution paths.
  Newline-terminated corruption is rejected. Successful acknowledgment depends
  on the storage implementation; it is not proof of hardware durability.
- Each custom receipt is at most 8 MiB. New receipt history is bounded to 4096
  records and 32 MiB; admission reserves room for both maximum-size records
  before I/O. Scope and call metadata remain bounded by the in-process runtime.
  These bounds do not add retention/deletion or a history migration mechanism.

Checkpoint/model-loop support, receipt-aware recovery, authorization transitions
and migration of the filesystem write stack remain separate work. No default
capabilities, shell/Python, Eino, CI, deployment, automatic retries or CAS of
external data are introduced.
