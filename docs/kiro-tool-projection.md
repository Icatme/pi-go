# Strict tool conversation DTO projection

This draft depends on #55 (and transitively #52/#51), not #53. It builds
community-shaped conversation DTOs only: history, current user, tools and tool
results. **It is not a sendable full request.** Origin/profile/conversation ID,
endpoint, credentials and transport are absent; no provider calls it.

Field shapes come from pinned community
[transform.ts](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/transform.ts)
and [stream.ts L993-L1011](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/stream.ts#L993-L1011).
No source implementation was copied, and this is not an official API guarantee.

## What is now projected

- Actual pi-go Context types, after #55's no-repair call/result identity check.
- Text user strings; assistant text and structured tool calls; text-only tool
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

Returned DTOs can be serialized with encoding/json without invoking custom host
methods. The schema and argument byte buffers do not alias caller-owned input.
Text-block boundaries collapse by concatenation, but no separators are invented.
Non-nil ToolResultMessage.Details is rejected, not silently dropped or serialized
as human/model text. Text appearing after a tool block is rejected because the
wire Content+Calls shape cannot preserve that interleaving.

## Deliberately unsupported

- Any SystemPrompt (including implicit lowering); dynamic system/catalog state.
  #53's separate text-only opt-in policy is not silently imported here.
- Images, reasoning/signatures, user content blocks, output schemas, constrained
  sampling, hosted tools/executions and namespace/provider ID repair.
- Missing raw argument evidence, nonnil parsed argument maps, unknown/duplicate
  schemas, interrupted tool groups or non-alternating projected turns.

Caller message/call budgets and #55 catalog/block limits apply. Aggregate text,
schema and argument bytes have an additional budget, checked before copying raw
schema data. Decoded content limits are not exact escaped-wire or heap limits.
No payload truncation or synthetic content repairs are performed.

## Validation and remaining gate

Unit-test sources cover empty result carriers, raw integers above 2^53, input
buffer isolation, error results, unsupported forms and budget rejection. A
panicking custom Tool.Parameters marshaler fixture ensures it must never be
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
