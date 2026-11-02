# Strict tool conversation DTO projection

The original tool projection depends on #55 (and transitively #52/#51).
This text-block extension is based on #61, which also includes #53's shared
text extraction helper. It builds community-shaped conversation DTOs only: history, current user, tools and tool
results. **It is not a sendable full request.** Origin/profile/conversation ID,
endpoint, credentials and transport are absent; no provider calls it.

Field shapes come from pinned community
[transform.ts](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/transform.ts)
and [stream.ts L993-L1011](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/stream.ts#L993-L1011).
No source implementation was copied, and this is not an official API guarantee.

## What is now projected

- Actual pi-go Context types, after #55's no-repair call/result identity check.
- Text user strings or text-only ContentBlock slices; assistant text and structured tool calls; text-only tool
  results. Results remain success/error with original IDs, in original order.
- Adjacent result groups become one user carrier with `content:""` and
  `userInputMessageContext.toolResults`. No Continue/Please-proceed text is added.
- Current tool catalog uses the observed toolSpecification/inputSchema.json
  nesting; catalog is attached to the final current user context. Historical
  tools must exist in that catalog; no placeholder schemas are invented.
- All resulting turns must strictly alternate user/assistant and end in user.
  Additional consecutive human speech is not silently merged or relocated.

## Explicit raw-evidence boundary

The caller must provide a schema map of raw JSON objects keyed by current tool
name. Tool.Parameters is deliberately not marshaled; arbitrary caller-owned
JSON marshaler/validator code is never invoked. This is explicit schema input,
not automatic conversion or a promise that the schema is valid JSON Schema.

Each tool call requires nonempty RawArguments **and Arguments==nil**. If the host
has both a parsed map and raw evidence (normal provider output often does), it
must explicitly decide which authoritative representation to project outside
this function. The function will not silently override potentially edited maps,
round large numbers through float64 or call arbitrary nested marshalers. Raw
JSON is strictly checked for object shape/duplicates/Unicode and copied.

`ToolCall.RawArguments` is ephemeral wire evidence marked `json:"-"` in pi-go;
it is not included in persisted conversation history. Restored tool-call history
without that evidence is therefore unsupported by this projection. Do not
re-serialize a parsed map (especially float64 values) and label it original raw
arguments: original number precision, spelling and duplicate-key evidence may
already be lost. Do not merely clear `Arguments` to pass validation. Selecting
raw evidence requires a deliberate host-level provenance decision that it is
still authoritative and has not been superseded by edited/validated arguments;
otherwise reject the replay rather than fabricate evidence.

A future evidence-retention design should use a versioned sidecar bound to the
session, message, call ID and provider, with a hash of the original byte content.
Its provenance must explicitly identify provider-wire evidence, not JSON rebuilt
from a parsed float map. Editing arguments must invalidate that evidence; replay
must verify the corresponding identities and byte hash before trusting it. A
hash binds content but does not by itself establish trusted provenance. Saving
such potentially sensitive arguments requires prior authorization and a privacy
review. This draft implements no sidecar persistence or credential recording.

Returned DTOs can be serialized with encoding/json without invoking custom host
methods. The schema and argument byte buffers do not alias caller-owned input.
Text-block boundaries collapse by concatenation, but no separators are invented.
Non-nil ToolResultMessage.Details is rejected, not silently dropped or serialized
as human/model text. Text appearing after a tool block is rejected because the
wire Content+Calls shape cannot preserve that interleaving.

This function is not a concurrency-safe snapshot operation. During the entire
call, the caller must ensure no other goroutine changes `ctx`, its nested message
or tool fields, or the schema map and its raw byte buffers. Returned JSON-buffer
isolation does not make concurrent mutation of the inputs safe.

## Deliberately unsupported

- Any SystemPrompt (including implicit lowering); dynamic system/catalog state.
  #53's separate text-only opt-in policy is not silently imported here.
- Images, reasoning/signatures, output schemas, constrained
  sampling, hosted tools/executions and namespace/provider ID repair.
- Missing raw argument evidence, nonnil parsed argument maps, unknown/duplicate
  schemas, interrupted tool groups or non-alternating projected turns.

Caller message/call budgets and #55 catalog/block limits apply. Aggregate text,
schema and argument bytes have an additional budget, checked before copying raw
schema data. Decoded content limits are not exact escaped-wire or heap limits.
No payload truncation or synthetic content repairs are performed.

## Validation and remaining gate

Unit-test sources cover empty result carriers, raw integers above 2^53, input
buffer isolation, error results, multiple tool groups with ordered multi-call
results, missing raw evidence, mismatched schema keys, unsupported forms and
budget rejection. A panicking custom Tool.Parameters marshaler fixture ensures it must never be
called. These tests have **not run**: no approved Go compiler/gofmt is available.

Before merge, run formatting, targeted tests, race/full tests with authorized
tooling. Do not register a provider or send these DTOs based on static review.
Full request construction still requires a verified allowed client identity and
endpoint/auth contract; no official CLI origin or user-agent is impersonated.

```text
gofmt -w pkg/pigo/provider_kiro_tool_projection*.go
go test ./pkg/pigo -run TestKiroToolProjection -count=1
go test -race ./pkg/pigo
go test ./...
```

## Text-block user inputs (integration branch)

On the #61 joined branch, user ContentBlock slices share the text-only projector's
strict extraction helper. Value/pointer TextContent concatenates in original order
without invented separators. Nil pointers, signed text, images, thinking and other
block types reject the entire projection. Empty/whitespace-only turns still reject.
User blocks now consume the same 4096 aggregate block budget as assistant and
result blocks; byte limits are unchanged. Input is not mutated. This capability
needs #53's helper present through #61, not #56 alone.

Additional regression sources cover string/block equivalence, pointer/value users,
current and historical turns, invalid blocks, exact shared block boundary and UTF-8/
byte limits. Go tests and gofmt remain unrun; this is not live provider integration.
