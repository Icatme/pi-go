# Strict host input preparation before explicit registration

Depends on #99. This pure helper is not registered and performs no authentication,
HTTP, tool execution or policy approval. It closes two unsafe shortcuts in the
old test-only agent fixture before building a public configured module.

Accepted inputs use either Context.SystemPrompt/Tools authoring fields, or exactly
one initial SystemMessage (value or nonnil pointer). Mixing both is rejected.
Initial static Content/Sections are rendered with the host's established sorted
section order; null section removal and ToolsRemoved are unsupported. Any later
system message is rejected instead of using resolveProviderContext to collapse it
out of chronological position. SystemPrompt stays a system string here: the
explicit request-level lowering policy is still mandatory.

The caller supplies a raw JSON schema catalog. Only currently declared tools are
selected. Each Parameters value must be a nonnil plain map[string]any matching
its raw JSON with the bounded exact-number evidence comparator. Native []string
arrays (such as schema.required) compare element-wise without conversion; nil,
wrong elements and non-string raw values reject. This shared comparator also
accepts that exact representation for tool arguments. No
custom MarshalJSON, validator, schema repair, float reserialization or tool
callback runs. Duplicate declarations, missing/mismatched/invalid raw schemas
reject. Extra unselected catalog entries are intentionally ignored and are NOT tool
authorization; only current declarations feed the response allowlist. Unsupported
Parameters representations are not silently converted.

The helper bounds messages (4096), declarations/catalog/sections (128 each), and
aggregate selected schema bytes (caller budget, maximum 1 MiB). System material
is bounded before rendering, conservatively counting section names/separators.
It copies selected raw bytes and slice containers, not arbitrary message/parameter
objects. Inputs must not mutate concurrently; downstream request serialization
must complete before invoking caller credential/transport callbacks. This is not
a complete request validator: existing request/history checks still apply.

Six independent original tests cover authoring/value/pointer initial forms,
explicit system policy, ambiguous/dynamic/removal semantics, hosted tools, exact
large integers, custom marshalers never called, malformed/duplicate/missing schema,
raw-buffer isolation, cyclic evidence, conservative separator budgets and a
no-tool initial system request with explicit lowering. These tests preserve local
host chronology rather than importing upstream automatic history repair.

Still needed: public opt-in config/registration, fresh request identity ownership,
explicit credential/executor injection, live service/account verification and
persisted raw tool replay. No automatic provider enablement is added here.
