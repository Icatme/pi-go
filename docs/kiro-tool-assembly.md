# Structured tool assembly: offline subset

This draft is stacked on PR #51. No provider is registered, no call is sent, no
credentials are read, and no tool is executed. Returned data is an internal
protocol object, not a pi-go executable tool call. Go checks have not run.

## Pinned community evidence

Reference: `mikeyobrien/pi-provider-kiro` at
`dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba`, not an official API contract.

- [stream tests L3023-L3078](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/test/stream.test.ts#L3023-L3078):
  an identified first tool frame, ID-less input continuation, then explicit stop.
- [stream.ts L1475-L1508](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/stream.ts#L1475-L1508):
  one current tool; usage fields merge rather than sum. `event-parser.ts` treats
  an empty input object as an empty fragment rather than the characters `{}`.
- [stream.ts L1525-L1585](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/stream.ts#L1525-L1585):
  upstream can retry after visible output and flush a pending tool at EOF.
  Neither behavior is adopted here.

These observable behaviors inform independent implementation; no upstream
implementation code is copied. Upstream license verification remains necessary
before future code reuse.

## Accepted subset

1. The caller supplies only payloads of classified `toolUseEvent` frames.
2. A start requires both nonempty name and ID. Identity strings are at most
   256 UTF-8 bytes. Only one tool can be active. Repeated identity must match.
3. Continuations may omit identity only while that sole tool is active.
4. Input is a string fragment or object. Empty object is a placeholder. Raw
   fragments preserve bytes and large numeric values; completed arguments must
   be one JSON object with no duplicate keys at any depth. Unpaired UTF-16
   escape sequences are rejected before Go can silently replace them with U+FFFD.
5. Only explicit boolean `stop:true` completes a tool. An empty input becomes
   `{}`. Complete-looking JSON without stop is never automatically published.
6. IDs cannot be reused within the assembler. Events, event bytes, tool count
   and accumulated argument bytes have explicit caller-provided limits. JSON
   nesting is limited to 64 containers. Limits bound input, not total heap size.
7. Any malformed/ambiguous input fails permanently. `finish` refuses an open
   tool and disallows further input. It does not declare the overall response
   successful; response lifecycle, usage and output emission remain separate.

## Deliberate compatibility gaps

Upstream accepts some tool sequences without an explicit stop, and flushes on
identity changes/EOF. This implementation is intentionally incomplete for those
sequences. A later response state machine may finalize them only after verified
completion evidence and full argument validation. Do not claim blanket Kiro
compatibility or treat every EOF as success.

The request mapper is also absent. Upstream repairs/reorders history, supplies
placeholder tool specifications and sometimes synthesizes user content; pi-go
needs explicit policies before adopting those transformations. No hidden text,
identity or tool-result fabrication should be introduced as an incidental fix.

Unknown fields fail closed in this subset. Future protocol extensions may need
reviewed relaxation. Tool-name allowlists, tool-schema validation, authorization,
user confirmation and actual execution belong to the host/agent, not this parser.

## Test source and validation gates

Tests cover empty-object placeholders, ID-less fragments, repeated/changed
identity, sequential tools, duplicate IDs/JSON keys, orphan fragments, null/wrong
field types, explicit stop, missing stop, byte/count budgets and terminal errors.
Fuzz entrypoints exercise nested JSON and two-event assembly independently.

No Go compiler/gofmt is available in the approved environment; **compilation,
formatting, unit tests, fuzzing and race tests have not run**. Static review is
not a substitute. Before merge:

```text
gofmt -w pkg/pigo/provider_kiro_json*.go pkg/pigo/provider_kiro_tool_assembly*.go
go test ./pkg/pigo -run 'TestKiro(JSON|ToolAssembly)' -count=1
go test ./pkg/pigo -run '^$' -fuzz '^FuzzKiroJSONObject$' -fuzztime=30s
go test ./pkg/pigo -run '^$' -fuzz '^FuzzKiroToolAssembly$' -fuzztime=30s
go test -race ./pkg/pigo
go test ./...
```

No CI workflow was dispatched and no toolchain installed for this work.
