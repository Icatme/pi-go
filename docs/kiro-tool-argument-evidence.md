# Dual tool-argument evidence

Normal pi-go provider ToolCalls often contain both `RawArguments` and a parsed
`Arguments` map. The earlier strict projection rejected every such pair. This
increment accepts supported pairs **only when they agree**, while keeping original
raw JSON as the sole request/replay source. It does not clear Arguments, serialize
parsed values as fake raw evidence, rewrite IDs or execute tools.

Plain map[string]any / []any trees, nil, booleans, strings, float64, json.Number,
int, int64 and uint64 are supported. Custom marshalers, struct/pointer values,
other typed containers/numbers and nested nil containers substituting for JSON
objects/arrays reject. Map sizes/keys, array order and scalar types must match;
comparison is bounded to 4096 values and depth64. The raw-only path retains its
existing strict JSON/byte/depth contract without these additional comparison caps.
Inputs must not be concurrently mutated. No caller marshaler or callback runs.

Raw numeric evidence is never converted to float64. For mathematically integral
raw numbers (including exponent/decimal spellings), parsed float64 must equal
the integer using its exact binary rational value, not its shortest spelling.
This catches large integers whose shortest spelling conceals a rounded binary
neighbor and permits exactly representable integers even when shortest spelling
differs. For non-integral raw numbers, a parsed float64 is rendered
as its shortest round-trippable decimal, then compared with the original decimal
using exact rational arithmetic. This is decimal round-trip agreement, not proof
that a decimal such as 0.1 is exactly representable in binary. Thus ordinary 0.1
is supported, but raw 0.10000000000000001 vs parsed 0.1, large integer rounding,
and underflow to zero reject. Equal spellings such as 1.00 vs1e0 compare equal,
and original 1.00 stays on the wire. Numeric comparison literals are limited to
128 bytes, with explicit exponent bounded to +/-1024; values outside this subset
reject rather than round. NaN/Inf and invalid json.Number text also reject.

Missing RawArguments remains unsupported, including normal persisted/reloaded
sessions (`RawArguments` is json:"-"). This is in-memory dual-evidence replay,
not durable provenance or a trustworthy persistence sidecar. Equality cannot
prove how a caller obtained raw evidence; caller provenance obligations remain.

## Independently authored migration and regression scenarios

Pinned community [transform.test.ts L146–164](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/test/transform.test.ts#L146-L164)
uses ordinary assistant tool-call arguments with adjacent tool results. The Go
projection regression now exercises that shape with a normal json.Unmarshal
parsed map **and extra raw evidence**, retaining the Go-only authority requirement.
It checks unchanged original wire spelling and no input mutation, then rejects
an edited map and missing raw. It does not claim support for upstream parsed-only
history or automatic history repair.

Further original tests cover matching nested Unicode/bool/null/array values,
json.Number/int64/uint64 beyond float exact range, equivalent decimals, rounded
large integers, decimal precision loss, underflow, nonfinite floats, string/number
confusion, custom marshaler nonexecution, nil-container differences, cyclic input,
duplicate keys, numeric work caps and traversal budget. Existing #77 conflict and
persisted-history regressions remain authoritative and run with this increment.
