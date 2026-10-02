# Managed MCP tool projection

`toolset.New` constructs an inert projection over a caller-owned MCP manager and
Codemode sandbox. Assign `set.Resolve` to `agent.AgentDefinition.ToolResolver`;
the core Agent continues to own the loop, message history, permissions, hooks
and cancellation. The package starts no model, credentials or alternate runtime.

```go
set, err := toolset.New(manager, sandbox, toolset.Options{
    Native: []codemodetool.Binding{codemodetool.Native(localTool, "local")},
    SnapshotScope: func(snapshot agent.AgentSnapshot) (mcp.Scope, bool) {
        // Read identity and auth epoch from trusted session/branch metadata.
        // Do not derive identity from prompts, model arguments or MCP names.
        return savedScopeForBranch(snapshot)
    },
})
// Handle err; set is scoped to the manager's trusted identity lifecycle.
definition.ToolResolver = set.Resolve
```

The manager's exact-name/tool-rule policy selects four exposure modes:

| Exposure | Model declarations | Codemode | `tool_search` |
|---|---|---|---|
| `direct` | Loaded before a model request | Callable | Already declared |
| `deferred` | Loaded only after selection/restoration | Callable | Searchable and loadable |
| `codemode` | Loaded only after selection/restoration | Callable | Searchable and loadable |
| `hidden` | Absent | Unavailable | Absent |

The constructor performs no MCP I/O. `Resolve` waits only for direct servers,
servers needed by restored selections, and already connected catalogs. Indirect
servers connect lazily when `code` or `tool_search` requests their capabilities.
Complete direct/selected declaration assembly has a 10-second deadline, shortened
by any caller deadline. A slow unavailable direct/restored server fails the
request explicitly; the package does not silently replace it with an empty
directory. Script/search setup uses the caller and configured managed bounds.
The code tool's prompt contains a compact globals/discovery reference, never
remote leaf schemas or server instructions. Each script captures its directory
before it runs. Dispatch still checks the current connection and permission;
identity/policy changes retire old connections rather than changing a live VM's
visible tools mid-script.

`tool_search` shares the sandbox's bounded BM25 implementation. It accepts
`{query,namespace?,limit?}` (default 5, at most the configured limit of 50).
Model search ranks only indirect tools absent from the current declarations;
sandbox `searchTools` still searches the whole allowed catalog. Search output
carries names and short descriptions; subsequent model requests
receive current declarations for selected tools. SDK connections, executable
functions, schema validators and tool authority are never restored from history.
`codemode.SearchTools` exposes the same metadata-only search to other Go hosts;
callers must filter unauthorized summaries before using it.

Live searches create bounded, opaque host selection records scoped to the
identity and authentication epoch. A record loads tools only when its successful
tool-result message is on the current transcript branch. Reusing a `SessionID`
does not share selections between unrelated branches. `Options.SnapshotScope`
is required to restore declarations after creating a new Toolset, restarting,
or compacting away live search records. A matching trusted saved scope allows
reconnection before executable restoration; missing/mismatched scope does not
restore indirect tools. Saved names select only tools currently allowed by the
manager and always obtain fresh definitions. System tool removals remain
authoritative. A scope change clears live records; stale executors cannot send.

Selection records default to 256 (maximum 1024). Exhaustion fails explicitly
without evicting an active branch or silently replaying a search. Allocate a new
Toolset for a new conversation and use trusted scope restoration when needed.
The frozen visible directory has at most 256 native/resource/MCP leaves; hosts
can narrow server policy or search namespaces rather than accepting an unlimited
catalog. Server and native namespace names must be distinct.

Saved MCP aliases use `Binding.NamespacePrefix()` for exact namespace matching.
Long/sanitized namespace identifiers contain a namespace-specific 12-hex hash,
so common long prefixes or `-` versus `_` names cannot reconnect each other's
server. Ordinary short names stay `mcp__server__tool`; exceptional namespace
aliases changed from the earlier combined-prefix hash and have no legacy shim.

Three virtual resource tools from `agent/mcpresources` are direct declarations
and native Codemode leaves in namespace `mcp_resources`: `list_mcp_resources`,
`list_mcp_resource_templates`, `read_mcp_resource`. `Options.Resources` sets
resource limits and an optional host artifact sink. Text/image/artifact results
use that package's structured schema; binary bodies are not copied into script
or model context. Resource definitions are recreated for the current identity,
and hidden servers remain unavailable. The sandbox and manager remain owned by
the application and must be closed by it.
