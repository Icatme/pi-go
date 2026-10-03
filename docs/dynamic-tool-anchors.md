# Native dynamic tool declarations (N3)

This work is stacked on N2, PR #22 at
`28f7ade22d1080fa892321ed686e752441664ee4`. It uses the existing system-message
tool declarations and Agent `ToolResolver`; it does not add another search,
catalog, permission, or restoration mechanism.

## Protocol and initial capability

Checked 2026-10-03. The initial target is only
`openai / gpt-6-astra / openai-responses`. The official
[tool search guide](https://developers.openai.com/api/docs/guides/tools-tool-search#add-tools-at-a-specific-point-in-the-input)
documents a developer-role `additional_tools` input item and requires preserving
its position during replay. The same guide uses `gpt-6-astra` for client tool
loading, and the exact [model page](https://developers.openai.com/api/docs/models/gpt-6-astra)
lists Tool search as supported on Responses. These are protocol/documentation
facts, not a live-provider test or a provider-wide capability grant.

The first slice anchors ordinary function additions. Initial tools remain in
the request's top-level `tools` array; later `SystemMessage.ToolsAdded` entries
become `additional_tools` items at those system messages. Native custom/grammar
additions use the existing current-tool-list fallback; initial custom tools can
coexist with later ordinary function additions. No synthetic tool-search calls
or namespace declarations are introduced. In an anchored request, a flat
function's returned namespace can only identify that same function; unrelated
namespaces must not resolve to a local function with the same short name.

## Replay and fallback boundary

The official addition item does not define removal or replacement semantics.
Any removal or same-name redeclaration therefore selects the complete current
tool list before dispatch, with no anchored additions. Unknown models, Codex,
other APIs, disabled mid-conversation system support, unsupported additions and
native-plan resource bounds select the same fallback. No HTTP-error-driven
representation retry is added.

On the selected native-capable model, dynamic history must be supplied in full.
Combining it with `previous_response_id` is rejected before payload hooks,
authorization or network activity, including when removal selects folded
declarations. This avoids relying on unverified removal of tools from inherited
server-side history. Static-tool requests keep the existing option behavior.

Anchoring is limited to 128 addition items, 256 added functions, 64 KiB per
encoded function and 1 MiB aggregate encoded functions. Exceeding these native
plan limits selects the existing top-level declaration representation. These
limits do not claim to bound an entire request's size or heap use.

Append-only additions must keep the already serialized input prefix and initial
tool list stable. Removal/redefinition intentionally changes that prefix; no
cache-hit promise is made. Namespace replay is tied to the selected declaration
plan and exact provider/API/model. A model switch or folded fallback drops
incompatible namespace metadata while retaining calls, results and call IDs.
The transcript remains the only source of tool state, including after JSON
round trips and Agent/session restoration.

## Verification plan

Use synthetic loopback Responses fixtures and the real Agent boundary for
addition, removal, identical and changed same-name declarations, ordered replay,
prefix stability, model switches and fallback. Cover static custom tools plus
dynamic functions, dynamic custom fallback, namespace isolation, serialization,
resource bounds and existing ordinary-function behavior. Run focused tests
first, then root/examples tests, race, vet and build. No paid provider request,
new credential, dependency upgrade, merge, release or downstream pin change is
part of this work.

The implementation and focused local regressions are complete. Five real Agent
scenarios cover resolver changes, execution, namespace rejection and snapshot
restoration without repeated execution. Provider fixtures cover declaration
position, serialized prefix, fallback, catalog refresh, bounds, namespace replay,
cancellation, partial/conflicting streams and HTTP 400 without representation
retry. Full repository checks and independent review remain pending at this
implementation checkpoint; no live acceptance is implied.
