# Injected HTTP to completed-tool queue (draft)

Depends on #91. Synchronous internal adapter, deliberately NOT a registered
provider or realtime tool stream: the function waits until the entire explicit
HTTP attempt and tool queue preflight have succeeded before returning a stream.
No stream exists during network reads or Close. No tool is executed.

## Boundaries

Explicit executor, credentials, profile/model/region/identity and tool schemas are
required; no defaults, discovery, redirects, retries or refresh are introduced.
Tool-name snapshots are validated before the executor runs. Request serialization
owns its bytes before the call, and each invocation uses independent state.

The HTTP core runs without a text observer: no previews of any kind escape.
Status/header/framing/CRC/strict JSON/tool terminal/EOF/wire budget/Content-Length/
response Close checks all precede projection and queue creation. Any failure
returns nil stream and one fixed error, not a raw transport/body/credential error.
Successful responses still must satisfy #89–91's strict semantics and limits.
Only TOOL_USE responses are accepted here; normal text termination is not yet
dispatched through a combined provider.

This function blocks while the injected executor/body/Close runs. Those trusted
implementations must cooperate with context; no goroutine can forcibly preempt
arbitrary code. No client, transport or tool callback is selected automatically.

The returned queue has the documented host cancellation contract: completion
evidence cannot be revoked by later cancellation. Tool execution must independently
check current context/schema/permissions; toolcall_end is not approval. A call
must originate from a legitimate caller and authorized service usage; constructing
this private runtime request does not establish official endpoint support.

## Independent synthetic tests

Five tests cover request tool declarations → fake binary response → exact
ToolCall/event queue → fabricated result → next-turn history; a gated response
Close proves no stream returns before Close, then a Close error still returns nil.
Truncation, declared length mismatch, unknown tool with valid CRC, cancellation
during Close, response+transport error all return no
queue. Invalid configuration/names/schema/credentials and budgets below the necessary
start-plus-abort floor never invoke the executor. Actual content-dependent queue
budget checks occur after the response; a large response can still be rejected.
Executor-time edits to source tools/schema cannot alter serialized request or
allowed-name snapshots; a later invalid attempt cannot inherit prior success.

No real service, credentials, billing, agent tool implementation or CI is used.
These synthetic scenarios extend the upstream tool lifecycle/transport migration,
not official captured wire compatibility. Registration, async combined text/tool
provider behavior and actual account/service validation remain separate.
