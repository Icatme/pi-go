# Explicit caller-authored tool history: first offline slice

The pinned community plugin accepts assistant tool arguments as an object (or
parses a caller-supplied string) and places that object into `toolUses[].input`:
[transform.ts lines 280–284](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/transform.ts#L280-L284).
It does not require the private Agent evidence field introduced in our draft #94.

The separate **explicit authored policy** projects current values into inert
Kiro request data. Controlled registration opt-ins are described below; existing
entry points remain strict. The Agent integration now removes the private draft bridge and restores its
original seven-field ToolCall layout; see [compatibility migration](kiro-agent-authored-compatibility.md).

## Two distinct contracts

- Existing/default original-evidence mode is unchanged: RawArguments is required,
  parsed values must agree if present, and original JSON spelling is replayed.
- New authored mode encodes the CURRENT `Arguments` object into request input.
  This generated JSON is not original provider evidence, is never placed into
  RawArguments, and makes no promise to preserve original whitespace/key ordering.
  A caller may intentionally edit its history. If RawArguments is also supplied,
  conflicts, malformed/duplicate JSON or mismatched values still reject; authored
  mode cannot silently override existing evidence.

Typed restored history is supported by this pure authored projector when its
numeric values remain exact (`json.Decoder.UseNumber`, int64/uint64). Ordinary
float decoding of a large integer is rejected, never "repaired" by guessing.
A serialized/reloaded tool call still has no RawArguments; it is accepted only by
the explicit authored policy, not promoted to original provenance.

## Encoding and safety limits

Root must be a non-nil plain map; empty maps produce `{}`. Accepted values are
plain map[string]any, []any, []string, string, bool, nil, json.Number, int, int64,
uint64 and float64. Nested typed-nil containers, custom types/marshalers, invalid
Unicode, cycles, excessive depth and excessive work reject. Only a detached
validated tree is marshaled; errors are fixed and do not include argument text.

Exact integers and json.Number never pass through float64. Nonfinite floats and
floating-point integers outside +/-9007199254740991 reject. Other finite floats
encode their current round-trippable value, not a claim of original decimal
provenance. json.Number has the existing strict 128-character / exponent +/-1024
limits. Numeric differences that were already rounded into an ordinary small
float cannot be detected without original evidence; do not claim otherwise.

Limits: at most 4096 values, 64 JSON containers, and caller byte budget with a
hard 1 MiB ceiling. Input string/key/number work is checked before encoding;
final JSON bytes including escapes are checked afterward. The byte limit is not
an exact heap-allocation guarantee. The overall projector additionally accounts
for these encoded argument bytes in its existing aggregate conversation budget.
Inputs must not be concurrently mutated; independent reads/copies are tested.
This is a data projection only, not tool execution permission or account identity.

## Acceptance and remaining work

Tests cover original/authored separation, edits, exact large numbers, serialized
UseNumber restore, rejection of rounded restore, duplicate/conflicting raw,
custom-method nonexecution, escaped-byte boundaries, node/depth boundaries,
shared acyclic trees, cycles, detached output and concurrent read-only use.
The scenarios are independently authored, not copied upstream test code.

The completed Agent integration and explicit external compile probe are documented
in [the compatibility migration](kiro-agent-authored-compatibility.md). This does
not introduce default network access or claim actual Kiro service compatibility.

## Explicit request/registration integration

The next slice threads the policy through the private request constructor and
public opt-in entry points, without adding fields to public configuration types:

- Buffered: `RegisterKiroBufferedModuleWithOptions(...,
  WithKiroCallerAuthoredToolHistory())`.
- Incremental text/completed tools:
  `RegisterKiroIncrementalModuleWithAuthoredToolHistory(...)`.

Existing registrations remain strict, including the existing incremental entry.
The new buffered option composes with explicit Auto HTTP and session affinity;
duplicates and invalid options reject. Live-text-only registration is unchanged.
No global policy, automatic fallback or argument persistence is introduced.

Request serialization and history validation still occur before credential lookup
or the injected executor. Authored/history errors and cancellation reach neither.
Tool schemas, chronological groups, allowlists, final-state checks and actual Agent
execution permissions are unchanged. Current caller history is input data, not
proof the model originally emitted it or that a tool should execute.

Fake integration tests cover buffered/incremental, full/simple host APIs, strict
rejection, explicit object authoring, edits, UseNumber restoration, rounded-value
rejection, conflicting raw, cancelled requests and callback counts. Wire assertions
parse the input's JSON number, rather than merely searching body text.

This request integration still does not remove the private Agent bridge: the
external ToolCall layout blocker remains until the separate Agent migration lands.
