# Optional durable local helper backend

M4c development slice, Linux only. `helper.Backend` implements the existing
`background.Backend` interface. It adds persistent operation identities and
explicit per-operation helper processes, without changing `background.Local`,
the coordinator, journal formats, or model tools. It is **not a sandbox, general
shell runner, daemon installer, remote executor, or production acceptance**.

## Explicit host setup

1. Build/install a trusted helper binary that calls `helper.Run` with fixed
   `NativeConfig`: native tool definitions, scope, policy revision and mandatory
   current-authority callback. Run receives the private operation directory as
   the last command-line argument. It obtains no tools or credentials from that
   directory. No executable is bundled or downloaded by this package.
2. Register absolute immutable binary paths, fixed arguments and tool names in
   `Options.Helpers`; provide scope, policy revision, capacity and `Authorize`.
   The host chooses everything except the ordinary tool request arguments.
3. Use `Create` **once** for a new private directory. Use `Open` for replacement
   coordinators; missing, partial or corrupt state fails closed. Never use Create
   to recover lost state. Keep old state until every operation has been reviewed.
4. Register the backend in the existing `superagent.BackgroundTool` grant, with
   its normal coordinator authorizer. This can also back the optional device
   adapter, but no device, listener, account or transport is configured here.

`Create`/`Open` pin scope, policy, capacity, tool list, absolute executable paths,
fixed arguments and SHA-256 binary contents in the registry header. Changing
them is not an in-place upgrade protocol. Executable bytes are checked again
before dispatch. The host must keep binaries and ancestor directories immutable
while operations are live; the hash check is not protection against a malicious
same-user filesystem race. A new backend grant does not inherit old operations.

The executable receives no inherited environment, stdin/stdout/stderr or extra
file descriptors. Linux starts it in a new session. It does **not** inherit a
worker's cancellation lifetime. The host remains responsible for OS accounts,
filesystem/network/resource isolation and parent-death/cgroup policies. Do not
register arbitrary shells or binaries that do not implement the helper protocol.
No detached descendants or untracked remote work are covered by native receipts.

## Identity, admission and recovery

The backend owns an exclusive, non-repairing JSONL writer. Every Start durably
reserves the immutable request and quota before preparing or spawning a helper.
Directory names are hashes, not model-controlled paths. Requests allow only the
registered tool, scope, call ID and JSON-object arguments (up to 64 KiB); parsed
arguments, namespaces and provider metadata are rejected at this boundary.

A request file binds the operation to its original authorization epoch. The
helper creates and syncs a write-once claim **before** native invocation. No
component removes that claim or resumes a claimed body after process failure.
Calling Start again with the same request never launches another process;
conflicting identity reuse fails. An uncertain Start must be queried by its
original ID, never retried with a new ID. Open, Inspect and Stop never dispatch.

Start waits at most five seconds (or caller cancellation) for a valid native
running/terminal snapshot after spawn. This is an admission handshake, not the
operation lifetime. Timeout leaves a possibly running operation, registry and
quota intact. Start errors can have committed effects. Stop may wait behind this
bounded handshake because commands serialize with authorization and persistence.

Snapshots are atomically replaced and file/directory-synced. Before returning a
fact, Inspect also appends it to the registry. Receipt identity and positive
committed/completed flags never regress. Missing, malformed, mismatched or
regressing helper snapshots preserve previously recorded facts and remain
unknown. Returned data is detached from internal state. Only a trusted terminal
snapshot proves captured native bodies exited and releases backend capacity.
An OS Wait merely reaps a child and withdraws liveness. PIDs and exit codes never
prove native exit, no effects, rollback, or safe retry.

After Open, nonterminal observations remain unknown until terminal evidence;
stale running files are not current liveness. A surviving helper may complete
normally, and the new backend can read its final result. Existing coordinator
unknown/review and quota barriers remain authoritative. No note can reconcile an
unproven exit. Closing a backend or coordinator does not stop its helpers.

## Authorization and stop boundaries

`Options.Authorize` runs under the registry lock on every command. It must check
the actual live grant/lease, not just return an epoch. The persisted highest epoch
rejects stale owners but is not a lease oracle. Start checks authority again just
before physical dispatch; a different epoch cannot adopt the old reservation.
The helper always passes the original admitted operation/epoch to its separate
`NativeConfig.Authorize`, including native pre-effect checks. Revocation cancels
the local invocation. A replacement coordinator can observe/stop an old operation
with current authority; it does not automatically grant the old helper a new epoch.

Native tools must call `ToolExecutionContext.CheckPermission` immediately before
their effects. The callback is composed with the host's existing native policy.
Callback capture, race-free authority enforcement and external-system fencing
remain host responsibilities. No lease can undo an effect already sent.

Stop appends intent before writing its marker. Inspect redelivers a pending
durable stop after recovery, checking current **stop** authority separately from
read authority. It records observations first so failed notification cannot hide
terminal evidence. Any present or unreadable stop marker cancels the helper;
corruption is never treated as permission to continue. Stop-before-Start creates
an unknown tombstone, never an exit claim; it permanently blocks that ID but does
not consume capacity for an operation this registry never admitted.

Cancellation and actual exit are separate. A tool may ignore cancellation and
report a late commit. Run keeps waiting until the captured native bodies exit,
including after storage failure, and never writes terminal evidence merely
because a context was canceled. Partial/late facts survive once durably observed.
**Local exposes a synchronous body's receipt handle only after its call returns.**
A helper killed before that point may lose a real commit's in-memory receipt.
The result remains unknown with quota held; this slice does not promise that
every pre-crash commit is durably captured.

## Limits and evidence

Capacity is 1–256; registered tools 1–64; receipt capture 256 per operation;
private output 64 KiB. Snapshot reads are bounded at 16 MiB to cover worst-case
JSON escaping of 256 bounded receipt identities. Records, artifacts, tombstones
and result history are not compacted. Do not put credentials in tool arguments,
fixed arguments, raw output, or the registry. Protect the entire private tree.

Tests launch only this package's own isolated test executable. They cover actual
coordinator-process death, helper-process death, replacement handles, duplicate
claim rejection, durable stop redelivery, cancellation-ignoring late commit,
native epoch revocation, quota/review retention and coordinator integration.
Synthetic record/snapshot fixtures cover reservation/request/claim crash points,
corruption, monotonic facts, maximum escaped receipt size and binding changes.
These are remote Linux process-crash tests, not host reboot, power-loss, adversarial
filesystem, Windows/macOS, real device or production validation. File and directory
sync calls do not substitute for storage-stack crash testing. See
[M4c](../../../docs/superagent-m4c.md) for validation commands and remaining scope.
