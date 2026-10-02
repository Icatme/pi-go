# Pi 1.0 follow-up: implementation through item six

The behavior reference is the exact upstream Pi v1.0.0 tag
(`a13d35a742c6ef8462812a28fbe1d8c8b7431c32`); the MCP protocol comes from the
already pinned official Go SDK v1.8.0. This is a Go library implementation,
with host-owned application UI, identity, permissions and network policy.

| Order | Follow-up | Implemented surface | Acceptance |
| --- | --- | --- | --- |
| 1 | Useful Codemode diagnostics and discovery reference | `codemode`, `agent/codemodetool` | Syntax/source locations, allowed-name suggestions, shared BM25, namespace/result descriptions, safe errors under the effective output/header budget |
| 2 | Real raw data and SDK cache binding | `mcp.Observer`, catalog snapshots, `mcptools.FromCatalog` | JSON/SSE/stdio, shuffled requests, exact numeric lexemes, cache hits/TTL/notifications, late-response rejection, no typed/raw fallback |
| 3 | Managed connections and lifecycle | `mcp.Manager`, HTTP/stdio, process ownership, dispatch checks | Shared setup attempt, explicit reconnect, identity retirement, session/owner-exit invalidation, Windows Job Object or Unix process-group cleanup, bounded repeatable Close, no tool replay |
| 4 | Dynamic exposure and restoration | `agent/toolset`, generic `ResolveChildTools`, `NewDynamic` | Four exposure modes, lazy indirect setup, hidden discovery/guessed-name denial, search selections isolated by branch, trusted-scope resume with fresh executors |
| 5 | Explicit OAuth and credential state | `mcp.OAuth`, Manager auth/logout, memory/file stores | Issuer/state/scope handling via SDK, account/client isolation, real version CAS, restored registration/scopes, single token refresh attempt, no auth-driven tool POST replay |
| 6 | Resource list/templates/read | `agent/mcpresources`, host artifact sink | Pagination cursor, scope/permission/cancellation, bounded text/blob/images, image projection into Codemode, inert generated artifact names |

Implementation review is split by the units that can build and be verified
together: sandbox feedback and generic lazy binding; managed wire/connection/
OAuth; dynamic projection and resources. The latter units depend on the former.
Source checks and precise verification evidence are recorded in
`PO_AGENT_WORKLOG.MD`; CI executes the same contracts on five native platforms.

The deterministic `examples/mcp-managed` executable uses a real loopback HTTP
MCP server and the ordinary Agent runner. It proves that indirect schemas are
absent initially, search loads a current deferred declaration, a generated-script
fixture batches three pages and filters them in the VM, a text resource is read,
hidden metadata stays absent, and only one bounded summary reaches the caller.
It makes no model-provider request and starts no OAuth UI.

## Execution contracts

- MCP catalog schemas, scope/epoch, connection generation, revision and wire page
  digest belong to the same snapshot. The SDK owns caching; observer invalidation
  prevents late pages from becoming a new SDK cache entry. A missing observation
  is an explicit failure. Large numeric schema constraints are rejected before
  float-based schema validation can round them; raw results remain exact in Go,
  while unsafe JavaScript Numbers are still rejected.
- A script receives a fixed allowed child snapshot. Host permission and live
  connection checks run again at physical HTTP/stdio handoff. Business failures
  retain `IsError`; local rejection, ambiguous remote execution, terminal remote
  response and `input_required` remain distinct execution facts. Errors retain
  original Go causes and an explicitly safe model presentation.
- Dynamic source/header deadlines include catalog setup, subsequent permission
  checks and VM work.
  Successful scripts share only the existing invocation-local store. Nested
  suspension is rejected and never restarts a script with earlier side effects.
  Runtime stacks and executable closures are not serialized.
- Unix stdio ownership ends at the assigned process group. Trusted servers must
  not daemonize or use `setsid`/`setpgid` to escape it; those servers require
  host-provided external containment. Windows owns the Job Object's process tree.
- Restore requires trusted saved identity and authorization epoch. Live search
  records apply only to the exact transcript branch where they succeeded. A
  reconnect rebuilds executable definitions from the current catalog; hidden or
  removed names cannot grant capabilities. Long/sanitized MCP aliases now contain
  a namespace hash; ordinary short aliases keep `mcp__server__tool`.
- OAuth is a host action. Model tools cannot invoke a browser/login, mutate
  identity or retry an authenticated POST. Refresh with unchanged scopes may
  continue the live connection; revocation/scope change retires it. Hosts own
  credential-file ACL/keychain protection and deliberate crash-lock recovery.
- Arbitrary binary resource bytes go only to an explicit host artifact sink.
  Text and supported images remain bounded. The client does not execute HTML or
  fetch linked assets; artifacts retain scope/server/URI/MIME/hash bindings.

Items seven onward remain outside this change: durable Codemode branch/store
transactions, Pi classifiers/image model APIs, and broader grammar/provider-auth
expansion. No live-provider verification, frontend UI acceptance, version bump,
release or automatic PR merge is implied by this implementation.

## Dependency and platform evidence

No dependency version is changed. MCP SDK v1.8.0 and wazero v1.12.0 are still
their latest released versions at the 2026-10-02 check. The existing OAuth and
Windows process-control dependencies become direct imports: `x/oauth2` v0.35.0
is the SDK's selected minimum; its repository remains active (v0.37.0,
2026-08-25). `x/sys` v0.48.0 was published 2026-08-31. The module requires
Go 1.26.2 and native verification uses Go 1.26.4. OAuth stays on the official SDK
path; a separate OAuth framework or native JS runtime is unnecessary.

Concurrent cold initialization reproduced wazero v1.12.0's unsynchronized version
cache ([upstream issue](https://github.com/wazero/wazero/issues/2532),
[merged upstream fix](https://github.com/wazero/wazero/pull/2536)).
The pinned QuickJS machine code is now cached for the process lifetime. Runtime
construction and cold compilation are serialized; VM execution remains parallel
with independent host imports, memory limits, state, cancellation and Close.
The upstream constructor fix is not in the pinned release, and no unpublished
dependency is injected. Concurrent-creation and runtime-isolation regressions
cover the application entry path.
