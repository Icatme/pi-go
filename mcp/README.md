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
Unix uses a process group. Shutdown terminates the owned process tree and waits
for actual exit. Stderr is privately drained with a bounded retention buffer.
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
Scope/revocation changes retire a live connection before another handoff.
`OAuthState` returns a credential version and granted scopes without secrets;
`ClearCredentials` performs versioned logout and retires the old epoch.

The default credential store is shared for the server within this Manager.
Persistence is opt-in via `NewFileCredentialStore(absolutePath)` in an existing
host-owned private directory. It uses a cross-process exclusive lock, real
version CAS, synced temporary file and replacement. Token files contain secrets;
the host supplies ACL/keychain policy and retention. A crash lock requires host
verification and removal; the library does not guess that another writer is dead.
