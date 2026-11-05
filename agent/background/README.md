# Controlled in-process background execution

`NewLocal(hostContext, scope, options, maxConcurrent)` wraps only the explicit
native tool allowlist in `options`. `Start`, `Inspect`, and `Stop` implement the
small `Backend` contract. No tool registration, shell, process, network adapter,
model invocation, scheduler, service, or external execution is added here.
Registered host tools retain their existing capabilities and must be trusted.
This is not a sandbox or an authorization grant.

- Use an explicit host lifetime, not a worker/dispatch context. Cancellation of
  a successful `Start` caller does not cancel its operation. Cancel the host or
  call `Stop` to request cancellation.
- Start validates scope, identities, allowlist and the bounded JSON request
  boundary before reserving an ID and a concurrency slot. The native runtime
  performs parsers, schema validation and permission hooks once on execution.
  IDs are immutable and process-local. Identical requests reuse the original
  operation; conflicting requests fail. Stop on a previously unseen valid ID
  stores an unknown tombstone and prevents later execution under that ID.
  Stop alone cannot prove that a lost operation in another instance exited.
  If the immutable Start request subsequently arrives at the same instance,
  it binds the tombstone and is rejected as exited/failed without entering a
  body. Repeated matching Start requests remain rejected; conflicts fail.
  Recovery must only inspect/stop historical IDs, never replay their Start to
  manufacture proof of exit in a replacement instance.
- `running` lasts until the receipt handle's `Wait` establishes that all actual
  executor bodies exited. Cancellation is not exit. Uncooperative bodies retain
  their quota and remain observed, even after the host context is canceled.
  There is deliberately no force-kill or timeout that falsely releases quota.
- Native call IDs are preserved. Receipt tokens are local to each operation,
  not globally unique. Snapshots retain actual parent links, domain, call/tool
  identity, commit and completion facts; hooks cannot rewrite those facts.
  The native handle is available only after the invocation returns. Until then,
  an empty receipt snapshot does not mean no executor entered or committed.
- Output is JSON of the native `ToolResult`, bounded to 64 KiB. Native Go-error
  outcomes have output omitted, preventing accidental raw error-string leaks.
  Encoding failures, encoder panics and oversized output also omit output and
  set `failed`, without dropping receipts. Explicit tool-provided result text
  remains host-controlled and is not a general secret-redaction mechanism.
- Inspect returns detached copies. `unknown` never proves absence of effects.
  An unexpected failure before the native receipt handle is returned quarantines
  identity/quota as unknown; do not automatically retry under a new identity.
- Registry/tombstones are volatile and retained for this instance's lifetime.
  They are not persisted, globally coordinated, evicted or recovered. Process
  loss destroys this evidence and cannot prove whether work committed. This
  package does not make arbitrary tools idempotent or enable automatic retries.

Requests are bounded to 1 MiB and each native capture to 256 executor receipts;
executor entry fails before effect when receipt capacity is unavailable. The
host owns callback synchronization and lifetime management. Inspect/Stop remain
available with a live query context after the host lifetime has been canceled.
