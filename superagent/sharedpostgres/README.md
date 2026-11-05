# Optional single-primary PostgreSQL journal

Development slice M6b. **Real PostgreSQL tests have not been run for this PR.**
Offline scripted-driver checks exercise client ordering and failure handling,
not PostgreSQL locks, transaction isolation, durability or network failures.
This is not a production-ready or cross-host deployment claim.

`sharedpostgres.Open(ctx, db, Options{Domain: "workspace-A", Owner: "instance-A"})`
returns a `superagent.LeasedJournal` for `superagent.OpenMainJournal`. The existing
JSONL and same-host SQLite APIs are unchanged. One owner holds a whole trusted
authorization domain; its workers may still execute different tasks in parallel.

## Host responsibilities

- Supply a configured `*sql.DB` and context. The package imports no driver,
  reads no environment/credentials, opens no connection string, starts no
  database and does not close `db`. The host controls driver configuration,
  connection limits, TLS certificate/hostname verification and authentication.
- Apply the exported `Schema` SQL **once**, using the host's migration role.
  `Open` and `ImportJSONL` do not install/upgrade tables. Runtime roles need USAGE
  on the schema, SELECT, INSERT and UPDATE on `pi_superagent_v1.domains`, and SELECT and INSERT
  on `pi_superagent_v1.transitions`. The schema/roles must remain host-controlled.
  Domain/owner labels are identities, not access controls against database users.
- Use one authoritative writable PostgreSQL primary (target versions 17/18 with
  supported patches) with durable storage, `fsync` and appropriate replication
  policy. Every transaction explicitly requests `synchronous_commit=on`.
  This does not make asynchronous replica promotion safe: losing committed
  epochs, admission decisions or receipts can revive old work. Read replicas,
  multi-primary routing, automatic failover and restore-to-an-older-state are
  outside this slice. Never delete/recreate live domain rows or reset counters.
- Select a context-aware `database/sql` driver. Client transaction deadlines and
  PostgreSQL transaction-local statement/lock/idle timeouts bound normal waits.
  A broken driver can still block an individual call; the independent ownership
  watchdog cancels callbacks without waiting for its I/O. Close itself waits for
  the operation gate only until its timeout and leaves the caller's DB open.
- Keep server clock changes controlled. `clock_timestamp()` is wall clock, not
  monotonic; backward jumps may delay takeover. Local monotonic deadlines limit
  an owner's lifetime regardless of server-clock rollback. Neither clock nor
  database fencing can undo an external action already sent to another system.

## Transaction and ownership protocol

Default TTL is 30 seconds; default transaction timeout is min(5s, TTL/4). TTL must
be 300ms–24h, transaction timeout 1ms–TTL/3. Tiny values are for failure tests,
not deployment recommendations. Full replay happens inside the claim transaction
and must fit its timeout; checkpoints/compaction are not implemented.

1. Begin a bounded READ COMMITTED transaction and set transaction-local timeouts.
2. Lock the domain row with `SELECT ... FOR UPDATE`. Claim inserts an absent row
   with `ON CONFLICT DO NOTHING` before locking. Only **after the lock returns**
   sample `clock_timestamp()` in a separate statement. Transaction-start `now()`
   and `CURRENT_TIMESTAMP` are deliberately not used as lease clocks.
3. Claim increments a durable positive bigint epoch. Append and renewal compare
   owner, epoch, expected sequence and current unexpired lease. Overflow is an
   error, never counter wrap/reset. Append writes record and sequence atomically.
4. After replay/insert/update, sample the server clock again immediately before
   COMMIT. A renewal must still be within the **original** grant at this point.
   Close only releases its exact owner/epoch while holding the row lock.
5. Advance the cache only after acknowledged, timely COMMIT. New local deadlines
   start before pool/gate/lock waits, never when the response arrives. A deadline
   timer uses a separate short-lived state mutex, never the transaction gate.
   A late renewal receipt cannot revive an expired/canceled handle.

Any attempted journal transaction failure cancels ownership conservatively.
`ErrBusy` means another live owner held a claim; `ErrLeaseLost` invalidates a
handle. `ErrCommitUnknown` means a COMMIT error or late receipt **may have committed**.
There is no automatic retry. Even another error after a successful database
commit may leave a durable record when local ownership expired concurrently.
Reopen/replay before deciding what occurred; do not blindly retry the append,
import, reply or external operation. Database atomicity excludes a replacement
owner while a transaction holds its row lock, but cannot make response delivery
instantaneous or undo an already committed row.

`Context()` cancellation reaches `OpenMainJournal` callbacks. Existing
`needs_review`, unknown-send and background-operation reconciliation barriers
remain authoritative. A stopped/partitioned old executor can still cause external
effects: hosts must validate live execution authority at the actual effect
boundary and reconcile against the original operation identity.

## Read-only JSONL cutover

`ImportJSONL(ctx, db, domain, sourcePath)` only targets an absent domain, using
one transaction with a 30-second maximum (or the caller's earlier deadline).
Stop old writers and keep them stopped through cutover. Source validation takes
the existing JSONL writer lock and refuses torn tails without repairing them.
Raw reads are also compared against the validated snapshot; the source is never
modified. The snapshot lock ends before destination commit, so quiescence is a
host precondition, not an online migration guarantee.

`bytea` preserves the original header and each LogItem JSON byte-for-byte,
excluding newline separators and batch wrappers. No JSONB conversion is used.
IDs, seq/parent/timestamps, payload number spellings, escapes and duplicate keys
are retained in stored bytes. Public detached structs have the same normal JSON
decode/clone semantics as the existing journal API. Keep the original file;
there is no dual write, automatic rollback, online migration or downgrade flow.

## Verification

Normal `go test ./superagent/sharedpostgres` runs offline scripts and skips every
real database test unless **both** `PIGO_POSTGRES_TEST_DSN` (explicit PostgreSQL
URL) and `PIGO_POSTGRES_TEST_DISPOSABLE=1` are supplied by the host. Never point
these tests at production. They install/check this schema and create/remove their
own random test domains. They do not start a database, discover credentials or
drop a schema. Remote URLs require `sslmode=verify-full`; explicit loopback
`sslmode=disable` is permitted for isolated test services. Implicit PG/home-file
configuration is cleared during parsing.

The gated tests use pgx/v5 v5.11.0 stdlib (MIT; Go >=1.25), solely as a test
dependency. They cover independent pools, same-name owner ABA, database fencing
of a locally live old handle, expiry while waiting for a row lock, real row-lock
timeout, server-side transaction abort, a deliberately injected lost COMMIT
receipt and raw migration/rollback. The receipt injection is **not** a real
TCP partition. Independent pools are **not** independent processes or hosts.

Still required before deployment: execute the suite on an authorized disposable
PG 17/18 service; add independent process kill/pause/takeover, actual TCP loss at
commit boundaries, database restart, disk durability and two-host fault testing.
No claims of those tests passing can be inferred from offline test success.
