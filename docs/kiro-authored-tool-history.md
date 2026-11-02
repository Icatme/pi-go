# Explicit caller-authored tool history: first offline slice

The pinned community plugin accepts assistant tool arguments as an object (or
parses a caller-supplied string) and places that object into `toolUses[].input`:
[transform.ts lines 280–284](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/transform.ts#L280-L284).
It does not require the private Agent evidence field introduced in our draft #94.

This change adds a separate **internal, explicit authored policy** to the inert
Kiro conversation projector. It does not enable that policy in HTTP requests or
public registration yet, change public struct layouts, or remove the current
Agent bridge. The ToolCall compatibility blocker is therefore **not fixed yet**.

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

Before calling the public compatibility issue resolved: thread an explicit
controlled option through request/registration, exercise actual fake Agent
multiple prompts and restores with precise argument conversion, then remove the
private ToolCall field and rerun the external seven-field compile probe plus all
legacy/non-Kiro tests. Do not remove the bridge first or claim this isolated
projector proves a complete Agent integration. No default network, credentials,
persistence expansion, service calls or upstream service guarantee is introduced.
