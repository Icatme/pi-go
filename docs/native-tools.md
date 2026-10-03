# Native custom tool inputs (N2)

`pigo.Tool.ConstrainedSampling` and `agent.ToolDefinition.ConstrainedSampling`
opt a tool into a Responses custom input. Execution still receives the ordinary
argument object. The parameter schema must have exactly one property, a required
string. The adapter infers that property; the model cannot select its mapping.

```go
tool.ConstrainedSampling = &pigo.ToolConstrainedSampling{
    Type: "grammar", Mode: "require", Syntax: "lark",
    Definition: "start: /[a-z]+/",
}
```

`Type: "text"` requests unconstrained native text. `Type: "grammar"` requires
`Syntax: "lark"` or `"regex"` and a nonempty definition. Grammar compilation and
language membership are provider responsibilities; local schema validation,
permission checks and sandbox limits still apply. A valid grammar does not make
tool input trusted. JSON-schema strict sampling is outside this change.

`Mode: "prefer"` (also the empty default) selects an ordinary function when exact
native capability is unknown or unsupported. `Mode: "require"` fails before
authorization, payload hooks or network activity. Hosts can inspect
`pigo.ResolveToolSampling(model, tool)` before calling the model. There is no
HTTP-400 fallback or automatic resend with a different tool representation.
Declaration encoding, history and decoding share the same resolved mapping for
each request; subsequent catalog changes affect the next request. Even malformed
custom output ends the existing pre-output retry window.
Malformed configuration or schema fails in either mode, including on an
unsupported model.

## Capability evidence

Checked 2026-10-03. The first built-in allowlist contains only the exact
`openai / gpt-6-astra` pair on `openai-responses`. The official
[function calling guide](https://developers.openai.com/api/docs/guides/function-calling#custom-tools)
uses this ID in its custom text, Lark and regex examples; the
[model page](https://developers.openai.com/api/docs/models/gpt-6-astra) identifies
the Responses endpoint. The
[Responses streaming reference](https://developers.openai.com/api/reference/resources/responses/streaming-events)
defines `response.custom_tool_call_input.delta` and `.done`, with `item_id`,
`output_index` and string `delta`/`input` fields. The
[create-response reference](https://developers.openai.com/api/reference/resources/responses/methods/create)
defines custom declarations, calls and outputs.

`ModelCapabilities.CustomTools` and `GrammarTools` are model-only facts. API or
provider defaults do not grant them. Unknown IDs, sibling models, Chat
Completions, and built-in Codex routes remain unavailable for native inputs.
A host can register an exact model with these facts when it has verified its
endpoint. Per-request `Model.Capabilities` overrides cannot grant support.
`GrammarTools` means the adapter can dispatch both documented Lark and regex
formats; it does not promise acceptance of every grammar.

Pi v1.0.0 (`a13d35a742c6ef8462812a28fbe1d8c8b7431c32`), particularly
[`constrained-sampling.ts`](https://github.com/earendil-works/pi/blob/v1.0.0/packages/ai/src/api/constrained-sampling.ts)
and the shared Responses adapter, is the behavior reference for string-to-object
mapping. The Go contract adds explicit required/preferred selection and keeps
the existing local validation and execution lifecycle.

## Streaming and replay

Custom input deltas become escaped JSON object deltas for existing consumers.
Quotes, backslashes, newlines, Unicode and empty input retain their exact string
values. Input done closes the argument JSON; a completed item or terminal output
must still finalize the call. Terminal-only responses work. Identical repeated
completion is idempotent; nonmonotonic input, conflicting completion, ambiguous
identity, unknown custom tools and unfinished calls fail the response. An error,
cancellation or length stop cannot dispatch partial tool input in Agent.
Output indices remain bound to one call, and native declarations cannot return
function calls. Namespaced custom output is rejected until N3 enables that
separate protocol. Invalid UTF-8 or unpaired surrogate escapes fail before JSON
decoding can change source text.

Calls retain `call_id|ctc_item_id`. Replay chooses the current tool representation
and pairs output by call ID. Item IDs are kept only for the same provider, API,
model and matching `ctc_`/`fc_` type. Model switches and custom/function switches
drop incompatible item IDs. Removed custom declarations replay as ordinary
function history. Native replay requires the exact single-string argument
mapping; incompatible historical arguments fail before sending.

Grammar and parameter schemas are limited to 64 KiB each. Native input is limited
to 1 MiB per call, 4 MiB total per response and 128 custom calls per response;
at most 256 native declarations are accepted. Existing transport-frame and
pending-event limits remain in force. These are byte limits, not heap/RSS claims.

## Codemode

Pass the same declaration through `codemodetool.Options.ConstrainedSampling`
to `New` or `NewDynamic`. Both still execute `{"code": source}`. The default
remains the ordinary function representation. Native text is useful for code
without imposing a language grammar; a host-supplied grammar is opt-in.
Codemode retains its existing 64 KiB source limit and invocation, child-call,
permission, timeout, store and journal contracts.

Verification uses synthetic loopback SSE/WebSocket fixtures and the real local
Agent/Codemode path. No paid provider call, new credential, provider-side grammar
compilation or live-model acceptance is claimed. N3 dynamic declaration anchoring,
dependency upgrades, release changes and downstream pins are outside this PR.
