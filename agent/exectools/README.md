# Host-injected execution tools

`CommandDefinition` and `PythonDefinition` create opt-in `run_command` and
`run_python` tools. They adapt a **trusted host function**, not a shell or Python
implementation. They never look up a binary, start a process, install Python/uv,
contact a service, or infer an isolation policy. A nil executor is rejected.
No core or filesystem preset enables them.

A practical application is a service that already owns a tenant-scoped job
runner: bind that runner and its fixed execution profile in a closure, then
expose bounded arguments or source through one factory. Applications without
such a backend do not gain execution by constructing an empty configuration.
Use an explicit receipt-aware Agent entry point and optionally
`codemodetool.Native(definition, "execution")`; the normal leaf permission path
still applies. No second Agent loop or provider SDK is required.

## Host contract

For `run_command`, the model supplies only `{"args":[...]}` (at most 256 args).
The host selects the executable. Empty args is allowed, including zero args;
individual empty strings are preserved. For `run_python`, the model supplies
only `{"code":"..."}`; source must be nonempty. Inputs must be valid UTF-8,
contain no NUL, and fit the total decoded-byte cap (default 64 KiB, maximum
256 KiB). Unknown JSON fields are rejected. The executable/backend, interpreter
version, cwd/workspace mapping, environment allowlist, credentials, tenant and
network/mount policy are **not model parameters**.

This does not make arbitrary arguments safe: programs such as interpreters,
build tools or version-control clients can execute code through their options.
A fixed executable is not an argument allowlist. Source/AST filtering, Python
`-I`, `os.Root`, an ordinary container, a Go context or the Codemode VM must not
be advertised as the external job's complete OS isolation policy.

The backend must enforce the actual platform policy, including filesystem and
secret exposure, network/DNS/metadata-service access, memory, CPU, process count,
disk growth, and process-tree termination/reaping. These controls are not
implemented or verified by this adapter. There is no fallback to unsandboxed
execution when a backend is unavailable. Remote backends also own upload,
credential, network, cost and job-lifecycle authorization.

Use separate scoped closures/environments per tenant. Prefer independent Python
invocations rather than sharing interpreter state across tenants. The wrapper
is portable Go, but supports only the platforms that the supplied backend
actually supports. No external backend has been certified by these mock tests.

## Dependent calls in one Agent batch

The factories leave `ExecutionMode` at the Agent's normal default. They do not
infer whether a host command/script and another tool are independent. When a
command produces state that a following read needs, opt that definition into the
existing ordered policy before adding it to the Agent:

```go
command, err := exectools.CommandDefinition(execute, exectools.Options{})
if err != nil {
    return err
}
command.ExecutionMode = agent.ToolExecutionSequential
```

The same assignment works for a `PythonDefinition`. If any tool in a normal
Agent batch requires sequential execution, the **whole batch** runs in model-call
order. This is not a per-command barrier with parallel segments. Keep the default
only when the host accepts concurrent independent calls; names such as `bash` or
`read` do not imply dependencies. This is in-process scheduling, not a transaction
or proof that a remote job has completed.

Code Mode has its own ordering: use explicit `await` between dependent calls, or
the sandbox's host-owned sequential option where appropriate. Setting an Agent
ToolDefinition mode is not a promise that arbitrary `Promise.all` scripts become
serial.

## Permission, cancellation and receipts

The tool requires a reserved capture slot before dispatch, validates input and
checks current context/`CheckPermission`. A nil callback is an explicit host
choice to permit this definition; capture is not permission. The backend is
called synchronously with an adapter deadline (default 30 seconds, configurable
up to ten minutes; an earlier caller deadline wins). Permission and context are
checked again before returning output. Revocation is not atomic with dispatch
and does not stop an already running process by itself.

The wrapper creates no background goroutine to pretend a job stopped. A backend
that ignores context can remain active indefinitely; it must define how it
kills/reaps work or reports an uncertain remote status. The Agent/Codemode
caller may already have returned on cancellation while the actual leaf is
still running. Retain the receipt handle and backend reconciliation identity.
`Receipt.Completed` means the Go executor exited, not that all descendants or
remote jobs have stopped. Do not detach work and report completion as though it
were an actual job-exit guarantee.

Neither process start nor exit code zero causes a committed receipt. A trusted
backend may call `executionreceipt.ReportLocalCommit(ctx)` immediately after a
specific confirmed local effect and must check its return value. Otherwise the
receipt remains unknown, including on success. Remote status is not converted
into a local commit. Committed facts survive later error, cancellation, panic,
after-hook or result-projection failure; a crash before reporting remains
unknown. Receipt capture is not a retry barrier or idempotency protocol. Models
and scripts can call again after failures; the host must decide whether that is
acceptable and supply its own policy. There is no rollback or automatic retry.

Supported paths are explicit in-process direct/model/Codemode capture and the
limited session-backed single direct-call binding. Persistent model journals,
checkpoint execution and legacy entry points cannot supply capture. The wrapper
adds no persistence format or recovery protocol.

## Output and failures

The backend writes stdout and stderr into separate writers sharing one raw-byte
budget (default 16 KiB, maximum 64 KiB). Concurrent writes are safe; ordering
between streams is unspecified. The prefix that fits is retained. Excess bytes
are discarded, `truncated` is set, and Write returns the original byte count to
allow pipe draining. This bounds the adapter's retained output, **not** backend
allocations, output generation, CPU or job duration. Writers return
`io.ErrClosedPipe` after the backend returns or panics and must not be retained.

The JSON result contains `exit_code`, `stdout`, `stderr`, `truncated` and
`encoding_replaced`. Invalid/incomplete UTF-8 in retained prefixes is replaced
with U+FFFD and flagged; NUL/control characters are JSON-escaped. The final
encoded object is independently bounded by `6 * MaxOutputBytes + 256` bytes.
This is a text diagnostic interface, not lossless binary/file transport. Output
is not redacted and may contain secrets, terminal escapes or untrusted text;
host/model consumers must treat it accordingly.

A nonnegative exit code is backend-reported; `-1` means no normal exit status.
Any nonzero code returns the bounded result with `IsError=true`, preserving
useful diagnostics in the Agent outcome. Codemode Native follows its existing
error projection: nonzero exit rejects the JS call, rather than resolving this
structured result. Do not assume JS catch receives separate stdout/stderr fields.
A backend error instead returns a safe generic tool failure
and discards output. The original cause remains available only to trusted Go
callers. Backend errors, post-dispatch permission/cancellation failures and
invalid exit status do not imply no effect. The wrapper does not infer a remote
execution state from a plain error.
