# Ephemeral provider raw-argument evidence

This draft closes the **in-memory adapter** evidence loss identified in #93.
It does not register Kiro, execute tools, establish official service compatibility
or add persisted-session replay.

## Representation

agent.ToolCall now has a private immutable pointer containing the original provider
RawArguments string and a SHA-256 change binding. It is captured only when a
provider actually supplies bounded, Unicode-valid JSON object text. No raw string
is ever manufactured by marshaling a parsed map.

The binding includes ID, OriginalID, name, namespace, thought signature, exact
current execution Arguments bytes and a typed fingerprint of ParsedArgs.
Fingerprinting accepts a bounded plain-JSON subset, preserves json.Number lexemes
and floating-point bit patterns, sorts object keys and never invokes custom
Marshaler/Stringer methods. Limits: 4096 values, depth64, 1 MiB aggregate string/key
payload; raw/execution arguments each <=1 MiB; each identity component <=4096 bytes.
Cycles, custom values, invalid numeric lexemes and exceeded limits suppress evidence.

On conversion back to pigo, matching evidence restores the original raw string.
Missing, changed or unsupported binding returns empty RawArguments. Current
Arguments/ParsedArgs are NOT replaced with old data. Kiro's existing strict
projector then rejects unsupported history rather than inventing original bytes.
Even a local-ID rewrite invalidates evidence in this conservative first version.
This is a current-value binding, not an irreversible edit history: restoring all
fields exactly to their captured values makes the original evidence match again.

This is origin/change evidence, not a signature or JSON schema check. Capture
checks grammar/Unicode/object shape but does not normalize or remove duplicate
keys, nor assert raw and parsed semantics agree. Provider-specific validation
(including Kiro's duplicate-key and dual-evidence check) still runs downstream.
A fabricated provider object is not authenticated by this mechanism.

## Clone, logs and persistence

cloneToolCall explicitly preserves the immutable pointer; ordinary message copies
preserve it as well. Public argument maps/bytes retain their existing clone behavior.
No changes are made to approval/checkpoint binding formats or execution parameters.

The field is unexported and omitted by encoding/json. ToolCall.UnmarshalJSON
replaces the whole value and clears any previous evidence, including decode into
a previously populated variable. Existing JSON bytes are unchanged; restored
sessions still lack original provider evidence and Kiro replay remains unsupported.

No logger is added. Tests check ordinary fmt output for ToolCall does not expose
the extra raw literal; direct evidence formatting returns a fixed redaction.
Existing public Arguments/ParsedArgs may still contain sensitive tool data, so
dumping ToolCall is NOT safe secret logging. No secure-memory erasure or protection
against unsafe/reflection access or deliberate package-internal access is claimed.

## Compatibility and limitations

Current provider execution Arguments and ParsedArgs conversions remain unchanged.
Existing non-Kiro request serializers do not consume RawArguments; the new metadata
is used by Kiro's strict request projection. No raw available means legacy behavior.
The prior parsed-map-missing reverse path still uses its existing decoder, so this
does not claim lossless arbitrary parsed-only history. New Kiro's nonempty
json.Number maps retain their precise in-memory path; edits/removal invalidate raw.

The original community transform tool-call/result scenario is independently
exercised through these adapter tests, not copied from upstream code. Tests cover
large integers, clone isolation, identity/argument edits, absent/empty evidence,
JSON restoration, debug privacy, cycles/custom method avoidance and execution
cancellation/permission/gate denial with a local counter-only fake executor.
No real model, tool side effect, credential, external service or CI is used.
