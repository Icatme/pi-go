# Tool-history group inspection, without repair

This pure utility consumes actual pi-go Context messages and returns only the
original assistant/result indices of structurally valid groups. It is **not**
a full request mapper, does not serialize arguments/results and never runs a
tool, validator, JSON marshaler or network request. Non-tool content is outside
this utility's validation; succeeding here does not mean a request is sendable.

## Evidence and subset

Community [transform.ts](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/transform.ts)
contains tool ID mapping and relocation of displaced tool results. Its
[history repair correction](https://github.com/mikeyobrien/pi-provider-kiro/commit/f6ec4bf4f9732a9f743524ec6c0b90e4199bbf15)
notes that relocating a result can move an intervening human utterance later.
The implementation here does not adopt repair, source code or client identity.

- Each assistant tool group must have explicit pi-go StopReasonToolUse and no
  assistant error. Each declared call is immediately followed by its matching
  result, in declaration order. Returned indices point into the original input.
- Interrupted, reordered, missing, orphan or extra results fail, even if an
  upstream repair algorithm could make them acceptable. No output is fabricated.
- Tool names must exist in the caller-supplied current catalog, whose entries
  are unique. Historical-but-removed tools are unsupported: no placeholder schema
  is invented. This is catalog identity checking, not tool-schema authorization.
- IDs must already match the community native ASCII syntax, length 1..64;
  non-native provider IDs are rejected rather than hashed. Reused IDs anywhere
  in the transcript fail. Namespace/thought-signature replay is unsupported.
- Result ToolName must match as well as ToolCallID. Empty names fail rather than
  being inferred. Error-valued tool results are legitimate paired results.
- Caller-supplied message/call limits, 256 catalog entries and aggregate 4096
  assistant-content blocks bound the work. No argument/result payload is traversed.
- Dynamic system/catalog changes and hosted tool executions are unsupported.
  No input is mutated and no partial index list is returned on failure.

Later mapping still needs explicit argument/raw-evidence rules, text/image and
result schemas, instruction precedence, model/profile/allowed origin identity,
and final authorization. A result-only current turn must not receive invented
human text. The text-only projection in #53 continues rejecting tool groups;
this utility does not silently change that behavior.

## Validation status

Static review found no blocker; test sources cover order, pointer/value types,
missing/extra/unknown pairs, duplicate catalogs and cross-group IDs, metadata
replay rejection, nil pointers, native ID syntax and aggregate budgets.
**Go compilation, formatting and tests have not run.** No CI, installation,
credentials or service calls were performed. This draft depends on the same
reviewed foundation through #52; keep it unregistered until checks pass:

```text
gofmt -w pkg/pigo/provider_kiro_history_groups*.go
go test ./pkg/pigo -run TestKiroHistoryGroups -count=1
go test -race ./pkg/pigo
go test ./...
```
