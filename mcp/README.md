# Managed MCP connections

`mcp` owns explicitly configured HTTP/stdio connections, raw response observation
and credentials. It does not depend on Agent or the model/provider package. The
host supplies its identity, server trust, network policy and OAuth presentation.
`New` is inert: it neither connects nor starts processes or a login flow.

```go
manager, err := mcp.New(mcp.Config{
    Scope: mcp.Scope{Identity: "host-account-id", AuthEpoch: 1},
    Servers: []mcp.ServerConfig{{
        Name: "issues", URL: "https://example.com/mcp",
        Exposure: mcp.Codemode,
        ToolRules: []mcp.ToolRule{{Pattern: "delete_*", Exposure: mcp.Hidden}},
    }},
})
// Check err, then choose when to Connect, Authenticate or assemble a Toolset.
```

The default exposure is `codemode`. `direct` tools are declared initially;
`deferred` tools load through search; `codemode` tools are immediately callable
inside scripts and can also be loaded through search; `hidden` tools are absent
from discovery and reject guessed names. Exact rules win over wildcard rules;
the first matching wildcard rule wins. Listing is never permission to execute.

For stdio, set `Command`, argument-vector `Args`, optional `Dir`/`Env` and
`Trusted: true`; there is no shell command interpolation. Windows uses a hidden
process and a Job Object, attaching the process before its first thread resumes.
Unix owns the assigned process group, not every possible descendant. A trusted
stdio server and its descendants must stay in that group: `setsid`, `setpgid`
or daemonization that escapes it is unsupported. Such servers require host
containment (for example a container or cgroup) instead of this stdio launcher.
Shutdown terminates the supported Job Object/process group and waits for the
owned server's actual exit. Stderr is privately drained with a bounded retention
buffer; inherited stderr handles cannot conceal that server's exit.
On macOS, a zombie-only group can report `EPERM` from `killpg1`; cleanup
checks `kern.proc.pgrp` and accepts only an empty group or exclusively zombie
members. A live member or inspection error preserves the original failure.
This follows the [XNU process-group signal implementation](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_sig.c).
HTTP endpoints require HTTPS or loopback HTTP. Redirects are disabled, MCP
multi-round-trip execution is disabled, and the SDK owns protocol negotiation.
The pinned SDK negotiates its supported current protocol with legacy peers.

`Connect` joins a single bounded attempt. A failed attempt remains an error until
the host explicitly calls `Reconnect`; tool calls are never retried. `Ready`
does no I/O. `SetScope` replaces actual SDK sessions and their observers, so SDK
caches cannot migrate between identities or authorization epochs. `SetExposure`
also retires the affected session. Connections captured by old executors cannot
dispatch after retirement. `Close(ctx)` can time out while cleanup continues;
another `Close` waits for the same cleanup and returns its result.
Unexpected SDK-session or owned-server exit also retires the connection and its
cached catalog. `Ready`/`Connect` report the closed session without restarting it;
only explicit `Reconnect` starts a new generation.

Header values and HTTP transports are trusted host configuration. Static
credentials must belong to that configuration: changing their account requires
a new Manager. OAuth credentials are selected by the current identity. A host
transport or cookie jar must respect context cancellation and the host's account
and network isolation policy; the library cannot turn an arbitrary RoundTripper
into a different authenticated account.

## Raw JSON and cache ownership

Each connection has a fresh Observer. Its SDK middleware marks a logical request
and binds the outgoing RPC ID to the actual JSON/SSE/stdio result bytes. Those
bytes remain associated with the exact SDK result object, including cache hits.
No typed serialization is accepted as replacement raw data. Missing, evicted,
invalidated or oversized observations fail explicitly.

`Refresh` always walks the SDK's list API, honoring both notification invalidation
and cache TTL. Catalog snapshots carry identity, epoch, connection generation,
revision and a digest of the real wire pages. Incoming notifications invalidate
the corresponding raw bindings and reject older in-flight directory/resource
responses before they can populate the SDK cache. Changed raw pages advance the
catalog revision even without a notification. Detached schemas and structured
results retain `json.Number` lexemes; JavaScript still rejects unsafe Number
values through the existing Codemode boundary.

`CallToolTracked` and `Track` return bounded dispatch facts. An accepted handoff
is counted at the transport byte boundary and does not prove socket delivery or
remote execution. RPC errors and ambiguous transport failures remain unknown;
only a trusted SDK lifecycle result reports completion or `input_required`.
`ResponseError` retains these facts if local raw/result mapping rejects a response.
`WithDispatchCheck` composes host permission checks at the actual handoff, outside
observer locks. Denials have zero accepted handoffs and preserve their Go cause.
An unsent stdio permission denial cancels only that logical SDK call and leaves
the shared session usable; physical write failures still terminate its writer.
Recent tombstones and compact exact numeric retirement ranges are independently
bounded by `WireLimits.MaxTombstones`. Numeric gaps stay available to requests
still serializing or awaiting approval; exhausting the range bound fails closed.
Repeated physical `tools/call` sends for a logical request are blocked, including
OAuth POST replay. There is no automatic recovery by repeating a script.

## OAuth

Configure `ServerConfig.OAuth` and an explicit `AuthKey.Issuer`. The official
SDK handles authorization metadata, PKCE, state and issuer checks, registration
and scope challenges. `AuthorizationCodeFetcher` belongs to the application;
this package starts no browser, callback server or UI. `Authenticate` is an
explicit host action using a protected-resource GET challenge, followed by a
new authorization epoch. Tools receive only the token-only handler: an auth
challenge returns `ErrAuthRequired`, without performing login or repeating POST.

Credential keys bind server, URL, host identity, issuer and client configuration.
Resolved dynamic clients and scopes are retained across restoration. Refresh
uses the previously successful client authentication style and cannot probe a
second token POST; failed or canceled refresh requires explicit authentication.
Before sending a refresh token, the store atomically publishes `RefreshPending`.
Restoring that credential or racing another OAuth instance cannot send it again;
only a successful result CAS or explicit authentication clears the claim. A
successful refresh advances `CredentialVersion` twice (claim and completion).
Token requests retain the SDK-validated MCP resource indicator, and restored
DCR clients retain their registered client authentication method.
Scope/revocation changes retire a live connection before another handoff.
The internal retirement fence completes before refreshed tokens become visible;
external `OnChange` callbacks still run after publication without internal locks.
`OAuthState` returns a credential version and granted scopes without secrets;
`ClearCredentials` performs versioned logout and retires the old epoch.
The credential CAS and Manager's epoch replacement share one publication
boundary. Existing sessions are canceled before CAS; even a failed CAS cannot
restore them, and their SDK sessions are closed outside the Manager lock. Login
network traffic and host callbacks also run outside that lock.

This unreleased credential structure now requires `Resource`. A test/development
record written by the earlier branch without it is rejected, including by
Manager login/logout entry points. The host must use the saved `AuthKey` and
current store version to CAS that record to a tombstone, then authenticate
explicitly; there is no inferred-resource migration.

When multiple registration strategies are configured, the derived client key
hashes all strategies, discovery override, redirect, resource and issuer. The
credential separately stores the client actually selected by the SDK, including
a preregistered or DCR fallback when the AS does not support CIMD. Restoration
reuses that client and its authentication method instead of selecting or
registering again. Earlier development keys for mixed strategies are not
migrated; omit `AuthKey.ClientID` to derive the new key and authenticate explicitly.

The default credential store is shared for the server within this Manager.
Persistence is opt-in via `NewFileCredentialStore(absolutePath)` in an existing
host-owned private directory. It uses a cross-process exclusive lock, real
version CAS, synced temporary file and replacement. Token files contain secrets;
the host supplies ACL/keychain policy and retention. A crash lock requires host
verification and removal; the library does not guess that another writer is dead.
