# Optional MCP tools

`Discover` adapts tools from an already connected official MCP Go SDK session.
The caller owns transport selection, network policy, credentials, deadlines,
session closure and current authorization. Importing this package does not
connect or launch a server. It never reads resources, fetches resource links,
requests prompts, configures OAuth, or starts stdio processes.

Create the caller-owned client with minimal capabilities and automatic
multi-round-trip handling disabled:

```go
client := mcp.NewClient(&mcp.Implementation{Name: "example", Version: "1"},
    &mcp.ClientOptions{
        Capabilities: &mcp.ClientCapabilities{},
        MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
    })
// Connect an explicitly approved transport, then defer session.Close().
tools, err := mcptools.Discover(ctx, session, mcptools.Options{
    Names: []string{"lookup"},
})
```

Nil/empty `Names` selects no tools and performs no I/O. There is no implicit
"all tools" mode. Missing or duplicate names, pagination limits and invalid
schemas fail discovery. Descriptions and outputs remain untrusted peer data.
Discovery is selection, not authorization. Supply current permission checks
through `CheckToolPermission` and approval decisions through `ToolGate`.
The same definitions can be supplied to an Agent or used without a
model through `agent.RunToolCall`; both support these checks. The adapter also
rechecks `execution.CheckPermission` after its directory/schema check, immediately
before `CallTool`. Use the current host-owned identity in this permission hook.
Do not put trusted identity or approval decisions in model-controlled arguments.

The adapter adds no retries. SDK 1.8.0 enables automatic multi-round-trip
retries by default, including load-shedding responses. A supplied session's
private configuration cannot be inspected here: the construction contract above
is required for the supported no-automatic-retry configuration. Disabling this
handler does not prevent an OAuth transport from resending a POST. Physical-send
attempts must be observed and controlled at the transport boundary; one SDK
`CallTool` invocation is not evidence of one network request. Configure no
sampling/elicitation/roots callbacks. Input-required responses are rejected;
the adapter never answers them. Transport/protocol errors are returned as Go
errors; tool-declared errors return `ToolResult.IsError` with their structured
content intact. `RunToolCall` retains the original Go error chain in
`ToolCallOutcome.Err`, including through after-hook redaction. `errors.Is` and
`errors.As` remain available to trusted Go callers.

`ResultError` means a remote response could not be mapped safely. Ordinary terminal
responses have `remote=complete_reported`; input-required responses remain
`remote=input_required` and are rejected without answering them. Neither state
means the operation failed or is safe to repeat.
Supported content is retained in its `Result` when within bounds. Transport
failures can also occur after execution; neither category should trigger an
automatic retry of a side effect.

Errors carry `agent.ToolExecutionError` codes, a stable reason, and separate local
and remote execution facts. Rejected arguments, changed schemas, and permission
denials remain `remote=not_dispatched`; transport/protocol failures from
`CallTool` remain `remote=unknown`. Rejected terminal results are
`result_rejected`, tool-declared failures are `tool_reported_error`, and
input-required responses are `input_required_unsupported`. Model text is a
presentation of these facts, not the source of retry decisions. After hooks may
redact presentation but cannot erase errors or change execution facts.

Input and successful structured output are validated with jsonschema-go,
without external schema loading. Structured output may be an object, array or
primitive; output declarations must be JSON Schema objects. Text and image
content maps to Agent parts. Other content, including embedded resources and
resource links, returns an explicit unsupported-content error and is never
fetched or executed. An error response need not match the successful output
schema. Metadata and annotations are not treated as policy or instructions.

The adapter preserves exact outbound JSON numbers (including nested int64 values)
using `json.Number`. Validation uses a separate native int64/uint64 or float64 projection because
jsonschema-go's type classifier treats json.Number as a string. Integers are
projected exactly; decimals must round-trip through float64 back to the same JSON
numeric value. Ordinary values such as 0.1 and 19.99 are supported. Integers outside
the native ranges or decimals requiring more precision fail explicitly before
invocation. Original wire values remain untouched. Numeric lexemes are bounded
to 256 bytes and exponents to an absolute value of 308 before rational conversion.
These checks prevent hostile exponent expansion. Validation follows
jsonschema-go v0.4.3 numeric semantics, not arbitrary-precision JSON Schema: its
float64 multipleOf division rejects some valid decimal multiples, including 19.99
with 0.01 and 0.3 with 0.1. Those calls fail before execution; schemas requiring
exact decimal arithmetic need a different caller validation boundary. The adapter
does not silently remove constraints or implement a second schema engine. SDK 1.8 decodes interface-valued schema/result numbers as
float64 before this adapter receives them. Numeric magnitudes at or above 2^53
are rejected conservatively, including genuinely representable values, because
an adjacent integer may already have rounded to that value. Such result rejection
is a post-execution `ResultError`; unsafe structured output is withheld while
exact text content remains available. Original numeric bytes cannot be recovered
from a rounded float. Small floating-point values retain the SDK's representation;
use string identifiers or exact text for protocols requiring lossless decimal data.

Before execution, the adapter re-lists through the SDK and compares the selected
tool's input/output schemas. A changed or missing tool requires rediscovery and
renewed caller approval. This follows the SDK's list-cache invalidation contract;
the caller must process tool-list changes and use an appropriately trusted peer.
It is not atomic server versioning and cannot prevent a malicious peer changing
behavior between listing and calling. Mutating the returned schema maps cannot
change the execution closure's validator.

Default bounds: 16 pages, 256 discovered tools, 64 KiB per schema/arguments and
1 MiB per result. These are checks after SDK decoding, not HTTP-body or memory
limits. Set transport limits and context deadlines separately. Cancellation is
forwarded to the SDK; remote termination is best effort. The adapter never closes
the session on cancellation.

For script orchestration, pass an explicitly selected subset to
`agent/codemodetool` as MCP bindings. That boundary additionally rejects unsafe
JavaScript numbers before invocation and checks effective results after hooks.
Only the code container is presented as a model tool; leaf capabilities stay
inside its approved child whitelist. Nested approval suspension is rejected
without restarting the script or replaying earlier side effects.

This optional package adds the official SDK v1.8.0 to the root module graph,
including its encoding, URI-template and x/* dependencies. It reuses the existing
jsonschema-go v0.4.3. Applications that do not import this package do not link its
MCP implementation. No new Agent runtime or protocol implementation is provided.
