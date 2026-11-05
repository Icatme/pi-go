# Device execution protocol (M7a)

This optional package adapts one explicitly selected, host-authorized device to
`agent/background.Backend`. It is a development slice, not a device fleet,
authentication service, production daemon, shell runner or completed M7.

## Embed explicitly

1. Supply a device-local `background.Backend` with an explicit tool allowlist,
   native permission checks, resource limits and a lifetime independent of any
   coordinator request. `background.NewLocal` is the offline reference backend.
2. Set `Binding` to non-secret domain, device, workspace and policy identifiers.
   `Workspace` is the backend's local scope, not an OS sandbox or filesystem grant.
   Supply the permitted tool names and a current-authority `Authorize` callback.
3. Use `Create` only when deliberately provisioning a new private registry. Use
   `Open` for every restart. Missing or corrupt existing state fails closed;
   **never recover by creating an empty replacement registry**.
4. Mount the returned `http.Handler` on a host-managed secure endpoint. The
   package opens no listener and obtains or saves no credentials. The host owns
   authenticated peer identity, TLS, deadlines, request/rate limits and isolation.
5. Build `Client` using the host's authenticated, non-retrying RoundTripper and
   the identical binding/tool list. HTTP is permitted only for literal loopback
   addresses used by fixtures; remote endpoints require HTTPS. Redirects and
   credential-bearing/query-bearing URLs are rejected. No default transport is
   supplied. The transport must not retry or add replay-enabling headers.
6. Register the client in M4 `BackgroundTool.Backend`, **using `client.Scope()`
   as its Scope** and the corresponding policy revision. The scope hashes the
   full domain/device/workspace/policy/tool grant, pinning the target in M4's
   existing durable operation record. Repointing the client changes the scope;
   old operations cannot silently move devices. The host still must preserve
   its tool-to-client mapping and perform M4 provenance authorization.

`ExampleNewClient` and `TestDeviceSharedJournalNativeCompletion` demonstrate the
external Go API without a real device, real provider or credential.

The Linux-only `durable_*_test.go` external-package fixtures additionally compose
the existing M6 same-host journal, an independent HTTP endpoint process and M4c
durable helper processes. The helper's scope is `Binding.Workspace`; the
coordinator grant is `client.Scope()`. They verify endpoint death, exact-ID
recovery, fixed-owner epoch fencing, lost acknowledgments, stop recovery and
private-result projection. See [M7b acceptance](../../docs/superagent-m7b.md).

## Authority and fencing

The bounded JSON POST protocol has version 1 and four commands: `describe`,
`start`, `inspect`, `stop`. Wire input carries no authoritative epoch or identity
claims. For **every** command, including queries, `Authorize` must authenticate
the caller and target, validate current live authority and return the exact
Domain/Device/Scope plus a monotonic coordinator Epoch. The callback runs under
the same lock as durable admission. An epoch smaller than the persisted maximum
is rejected even after process restart. Reads advance the fence too.

Highest-seen epoch alone cannot recognize an expired owner before its replacement
contacts the device. The callback therefore must consult authoritative current
ownership; merely trusting a header, previously valid signature or cached epoch
is insufficient. The integration fixture binds a current M6 journal Context and
Epoch; its synthetic headers are not production authentication. A distributed
host must supply its own authenticated current-authority mechanism.

Backend Start/Inspect/Stop run under this admission lock and must be bounded
admission/query calls, not long-running tool bodies. Actual work runs independently.
Fencing rejects new stale commands; it cannot undo an already admitted effect.
Native tools must recheck permission immediately before effects and accurately
report receipts. Device-side descendants and durable capacity remain the backend's
responsibility, including when replacing it. M4 retains its own durable quota;
this adapter does **not** add device-global durable quota across independent
coordinator journals or prove old descendants exited after backend replacement.

## Identity, reconnect and facts

A Start persists the operation ID and immutable request digest before calling
the backend, then persists acknowledgment before replying. Reusing that ID with
different arguments, call ID or tool is rejected. An identical duplicate never
calls Start again. Stop persists intent before requesting cancellation, including
permanent tombstones for unknown IDs. A prestart tombstone cannot dispatch later.
Stop intent is not exit evidence and does not erase committed facts.

Direct `Inspect` does not redeliver a Stop that crashed between this registry's
intent and the backend call. A direct host must retain that intent and explicitly
repeat `Stop` for the same ID. The existing M4 coordinator's recovery watcher
already repeats its durable stop intent until acknowledged. Once the helper's
own stop is durable, helper inspection can redeliver its notification under
separate current stop authority. These are distinct persistence boundaries.

On a lost connection the client reports an uncertain/unavailable result. It never
retries Start, changes ID, selects another device or resends on reconnect. Query
the original ID. Unknown is a review barrier, not proof of absence or permission
to retry. M4 recovery already follows this rule and retains its quota. `Describe`
reports the pinned device's capabilities and point-in-time reachability, not an
ongoing online guarantee. Hosts can label offline work with that configured device;
automatic waiting queues, device selection and reconnect scheduling are later work.

Restart never replays Start. Running cache records are durably made unknown before
Open returns. An unchanged persistent terminal observation may be replayed; other
operations require exact-ID evidence from the configured backend. A fresh volatile
backend normally returns unknown. A stop tombstone with no original accepted
request remains conservative even if later bound to the prevented request.

Every observation is saved before delivery. Device-private receipt fingerprints
and flags enforce stable identity and monotonic evidence, including unknown-state
merges. Missing or regressing receipts cannot establish exit. Exported results are
**facts only**: state, failed, and one synthetic root receipt indicating any known
commit and authoritative exit of all native bodies. They are not per-child results,
proof of success, rollback guarantees or exactly-once external effects.

Raw arguments are used transiently for dispatch; the registry stores their digest.
Raw output, errors, panic values and arbitrary child receipt strings never cross
the protocol. Credential access stays in device backend closures. No raw result
payload or artifact transfer is provided in this slice. Non-secret operation/call
IDs, tool names and binding identifiers are approved metadata, not secret channels.

## Storage and validation limits

The registry uses the existing JSONL writer lock, fsync-per-record and a new strict
non-repairing open. It is exclusive to one device endpoint process and must reside
in a host-owned private directory on a reliable local filesystem. Unix requires
private directory/file permissions (normally 0700/0600); Windows requires equivalent
host-managed ACLs. Symbolic-link files and public Unix files are rejected. Do not
replace, edit, delete, roll back, share over NFS/SMB or recreate this registry.

Explicit transition kinds reject impossible replay histories. Any uncertain write
poisons the endpoint. Close waits for in-flight handlers but does not wait for or
claim exit of native bodies. Registry creation does not sync the parent directory;
validation establishes process-crash behavior, **not power-loss durability** or
protection against hostile filesystem mutation. Registry history is unbounded;
compaction, rotation and migration need a separate identity-preserving contract.

Requests/responses are bounded to 96 KiB, Start argument JSON to 64 KiB, tool
allowlists to 64 names/64 KiB encoded configuration, and receipts to 256. All tests
are offline. See [M7a acceptance](../../docs/superagent-m7a.md) for verified scope.
