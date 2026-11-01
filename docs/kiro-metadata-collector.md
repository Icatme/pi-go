# Offline metadata consistency collector

This draft depends on #52's strict JSON helper. It accepts only already
classified `metadataEvent` payloads; no provider calls it and no network,
credentials, tool execution or response completion is enabled.

## Evidence

At community revision `dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba`,
[event-parser.ts](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/event-parser.ts)
maps `tokenUsage.uncachedInputTokens`, output, total, cache read/write, context
percentage and normalized usage. `meteringEvent.usage` is credits, not tokens.
[stream.ts L1496-L1508](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/stream.ts#L1496-L1508)
merges partial metadata fields rather than summing them. It may overwrite repeat
fields; this draft deliberately chooses a stricter conflict policy.

## Contract and compatibility limits

- Six known uppercase stop reasons only. Same reason repeated is accepted;
  a different subsequent reason permanently fails. A later END_TURN cannot
  overwrite CONTENT_FILTERED and re-enable tool release.
- Token fields are present/nonnegative int64 values, without float conversion.
  Zero is retained. Overflow, fractions, exponent notation, strings and null are
  rejected. The exponent restriction is a client subset, not a service rule.
- Separate partial token fields merge without summing or estimating missing
  fields. A different repeated count fails instead of being presumed cumulative.
  Wire total remains independent; no arithmetic total is fabricated from parts.
- Context percentage must be finite and in [0,100]; normalized usage must be
  finite/nonnegative. These ancillary values use float64, not token/billing
  counts. Repeated fractional fields must also have identical trimmed numeric
  spelling, preventing rounding from concealing a conflict. Even mathematically
  equivalent `50` versus `50.0` is conservatively rejected on repetition.
  Sign and percentage upper bound are checked exactly in decimal spelling
  before float conversion. Negative signs (including -0) and exponent notation
  are deliberately rejected, preventing underflow/rounding from bypassing bounds.
  Ancillary positive decimal values below float64 range may round to zero; this
  is documented loss in auxiliary estimates, never in integer token counts.
- Unknown fields, credits/metering payloads, wrong shapes, nested duplicate keys
  and malformed JSON fail closed. `stopDetails` must be an object but is discarded
  rather than stored/logged. No model content or raw error body is returned.
- Caller supplies event-count and per-event-byte limits; JSON nesting is bounded
  by the helper. This is not a total heap budget.
- Error state is sticky, clears previous success evidence, and returns no usable
  snapshot. Successful snapshots copy maps/scalars so callers cannot mutate state.

This does **not** prove whole-stream completion. A snapshot obtained before a
later failure must never be used to release tools. The future evidence collector
must consume the whole stream, combine cancellation/transport/tool state, and
apply the completion policy from #52. Unknown vocabulary and legitimate changing
cumulative metadata may currently be rejected; expand only with reviewed evidence.

## Validation status

Tests cover split metadata, exact integers above 2^53, zero presence, alias-free
snapshots, conflicting terminal reasons/counts, floating-point-rounding conflicts,
wrong numeric types/ranges, credits separation, size/count budgets and sticky
failure. Fuzz source checks terminal-state invariants.
Additional regression sources cover MaxInt64, `50` versus `50.0`, discarded
stopDetails, success-state rollback, both detached scalar pointers and long
decimal byte budgets. These are written tests, not executed evidence.

Go compilation, formatting, tests, fuzz and race checks have **not run**. No
toolchain was installed and no CI/live calls were initiated. Before merge:

```text
gofmt -w pkg/pigo/provider_kiro_metadata*.go
go test ./pkg/pigo -run TestKiroMetadata -count=1
go test ./pkg/pigo -run '^$' -fuzz '^FuzzKiroMetadataTerminalState$' -fuzztime=30s
go test -race ./pkg/pigo
go test ./...
```
