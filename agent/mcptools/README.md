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
Discovery is selection, not authorization. Wrap each returned `Execute` with
your current permission and approval checks before giving it to any caller.
The same wrapped definitions can be supplied to an Agent or used without a
model through `agent.RunToolCall`. The latter has no `ToolGate` option, so a
gate used by an Agent does not protect direct invocation. Do not put trusted
identity or approval decisions in model-controlled arguments.

The adapter adds no retries. SDK 1.8.0 enables automatic multi-round-trip
retries by default, including load-shedding responses. A supplied session's
private configuration cannot be inspected here: the construction contract above
is required for the supported no-automatic-retry configuration. Configure no
sampling/elicitation/roots callbacks. Input-required responses are rejected;
the adapter never answers them. Transport/protocol errors are returned as Go
errors; tool-declared errors return `ToolResult.IsError` with their structured
content intact. `RunToolCall` represents Go execution errors as error outcomes,
so use `Execute` when your workflow needs the original Go error chain.

`ResultError` means the remote tool already returned but its result could not be
mapped safely. It does not mean the operation failed or is safe to repeat.
Supported content is retained in its `Result` when within bounds. Transport
failures can also occur after execution; neither category should trigger an
automatic retry of a side effect.

Input and successful structured output are validated with jsonschema-go,
without external schema loading. Structured output may be an object, array or
primitive; output declarations must be JSON Schema objects. Text and image
content maps to Agent parts. Other content, including embedded resources and
resource links, returns an explicit unsupported-content error and is never
fetched or executed. An error response need not match the successful output
schema. Metadata and annotations are not treated as policy or instructions.

The adapter preserves exact outbound JSON numbers (including nested int64 values)
using `json.Number`. SDK 1.8 decodes interface-valued schema/result numbers as
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

This optional package adds the official SDK v1.8.0 to the root module graph,
including its encoding, URI-template and x/* dependencies. It reuses the existing
jsonschema-go v0.4.3. Applications that do not import this package do not link its
MCP implementation. No new Agent runtime or protocol implementation is provided.
