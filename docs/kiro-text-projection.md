# Text-only request projection (not a live request)

This isolated step consumes actual pi-go `Context`, `UserMessage` and
`AssistantMessage` types. It returns only history/current-message content and
model identifiers. It does **not** include origin, profile, conversation ID,
endpoint, transport or credentials and cannot be sent as a complete Kiro request.
It is unregistered and no production provider calls it.

## Evidence and intentional restrictions

The community request nests `userInputMessage` and `assistantResponseMessage`
inside history, with a final current user message. See
[stream.ts L993-L1011](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/stream.ts#L993-L1011)
and [transform.ts](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/transform.ts).
These are community observations, not official API support guarantees.

- Accept nonempty UTF-8 text-only turns, strictly user/assistant alternating,
  starting and ending with user. Preserve text bytes and chronology. Multiple
  text blocks concatenate without invented separators, as pi-go text content.
- Reject tools, results, hosted execution, images, reasoning, signed text,
  dynamic system messages, incomplete/error assistant turns, unknown content,
  empty turns and unsupported order. No repair, reordering, padding or dropping.
- `Context.SystemPrompt` is **rejected by default**. Only explicit
  `allowSystemLowering=true` prepends it once to the first user as
  `system + "\n\n" + user`. `SystemPromptLowered=true` marks this semantic
  downgrade; it is not equivalent to preserving system-role precedence.
- A context with tool calls/results is unsupported, even a structurally valid
  tool group. Do not silently flatten tool results to human speech. A future
  mapper must retain each call/result pair in chronological order, handle a
  result-only current turn with empty text, and explicitly reject interrupted
  or orphan groups rather than fabricate missing partners or specifications.
- Caller-provided limits bound message count and aggregate decoded UTF-8 text
  bytes (including lowered system prompt/separator). They do not bound all heap
  allocations or JSON-escaped wire length. Model ID is limited to 256 UTF-8 bytes.
- Input objects are not mutated, and failed projection returns no partial output.

Upstream has historically inserted placeholders and relocated displaced tool
results; [the corrective change](https://github.com/mikeyobrien/pi-provider-kiro/commit/f6ec4bf4f9732a9f743524ec6c0b90e4199bbf15)
clarifies that tool-result turns need no fabricated user text. Its relocation
still changes the ordering of intervening user speech. Neither repair is adopted.
Community `origin: KIRO_CLI`/CLI user-agent values are deliberately absent: a
permitted third-party identity and endpoint/auth contract must be established
before live integration. This code does not impersonate the official client.

## Checks

Tests are provided for value/pointer message types, Unicode text, text blocks,
chronology, explicit/default system policy, byte boundaries, unsupported inputs
and no partial returns. They have **not run**: the approved environment has no
Go compiler/gofmt. No installation, CI dispatch, live request or user-machine
operation was performed. Keep draft until approved validation is available:

```text
gofmt -w pkg/pigo/provider_kiro_text_projection*.go
go test ./pkg/pigo -run TestKiroTextProjection -count=1
go test -race ./pkg/pigo
go test ./...
```

This text-only projection is not full Kiro support. Tool-history mapping,
reasoning replay, allowed authentication and response completion remain gated.
