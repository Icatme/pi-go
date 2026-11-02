# Agent ToolCall compatibility and authored history

This integration removes the private providerEvidence field introduced in draft
#94 and restores the original seven exported fields. The external, intentionally
unkeyed literal probe remains in agent/testdata/compat_toolcall and is run
explicitly; it is not hidden by disabling vet. No global table, hidden session
owner, new metadata, or secret/raw persistence is substituted.

## Contract and migration

Agent histories carry their current execution argument object, not original
provider JSON provenance. Use WithKiroCallerAuthoredToolHistory for buffered
registration or RegisterKiroIncrementalModuleWithAuthoredToolHistory. Existing
direct pigo strict registrations still require original evidence and are not
silently relaxed. Agent users of those strict drafts must opt into the authored
path: removing the private bridge deliberately does not preserve strict raw replay.

Current ParsedArgs, including a nonnil empty object, takes precedence over
Arguments just as ordinary execution does. The Arguments-only fallback uses
UseNumber and rejects invalid Unicode, nonobjects and trailing JSON. Conversions
never manufacture pigo.RawArguments. Custom tool parsers remain caller-defined;
this history contract does not claim to reconstruct their transformed output.

Tool-specific clones preserve absent versus empty ParsedArgs. Its JSON tag uses
omitzero (supported by the required Go toolchain): nil remains omitted, but a
nonnull empty map is encoded as {} so restore cannot resurrect stale Arguments.
This is an intentional empty-object serialization correction; the public field
layout is unchanged. No MarshalJSON method is added, preserving outbound JSON
behavior for external structs anonymously embedding ToolCall. The preexisting
UnmarshalJSON method and its embedding behavior are unchanged.

The tag change is **not** complete source or serialization compatibility: Go
includes tags in struct type identity, so direct assignment to an anonymous
struct repeating the old `parsed_args,omitempty` field no longer compiles.
Use the exported `ToolCall` type, copy fields explicitly, or use an explicit
conversion where Go permits it (struct conversions ignore tags). Reflection
consumers and byte-for-byte JSON fixtures must also account for the changed tag
and empty-object output. Third-party JSON encoders must be checked separately
for `omitzero` support; the tested persistence path uses `encoding/json`.

Old snapshots that already omitted an empty `ParsedArgs` object cannot reveal
whether it was absent or an intentional empty edit. Do not bulk-rewrite absent
values to `{}`: that would suppress legitimate `Arguments` fallback. Reconstruct
such edits only from caller-owned authoritative data, not inferred provider
provenance.

For existing buffered Agent registration, replace the strict registration with
`RegisterKiroBufferedModuleWithOptions(source, config,
WithKiroCallerAuthoredToolHistory())`, retaining any explicit session-affinity or
AutoHTTP options already in use. For incremental Agent registration, replace
`RegisterKiroIncrementalModule` with
`RegisterKiroIncrementalModuleWithAuthoredToolHistory` using the same config and
options. Do not migrate direct `pigo` callers that intentionally require original
wire evidence. Neither option starts networking during registration.

Exact json.Number/int values survive current-object projection and JSON restore.
Unsafe large float integers reject rather than guessing their original value.
Edits are caller-authored and are not certified as provider output. Discarded raw
whitespace, duplicate fields and unknown raw values are never newly persisted.
Already rounded small/fractional floats cannot be recovered or detected reliably.

## Validation scope

Synthetic tests exercise actual public registration → Agent Prompt → another
Prompt → FollowUp/Continue → JSON snapshot restore → Prompt, five HTTP fixtures
and exactly one counter-only tool execution. Historical input remains a JSON
number 9007199254740993, with fixed tool ID/name and stable affinity. Tests cover
current edits, empty-map clone/restore precedence, zero/embedded JSON output,
permission denial, cancellation, truncated streams and discarded raw nonexposure.

No real credentials, service calls, tool side effects or account authorization
were used. These tests prove local contracts, not real Kiro endpoint acceptance.
Inputs must not be concurrently mutated. Child-tool and reflection clones also preserve empty objects; the existing
session reducer already did. Custom third-party adapters retain their own contracts.
