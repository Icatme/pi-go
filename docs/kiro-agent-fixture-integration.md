# Historical fixture design (superseded argument contract)

The raw-evidence/restore-rejection descriptions below record the original draft.
Current tests use explicit authored history, never invent raw, and support precise
JSON restoration. See [current migration](kiro-agent-authored-compatibility.md).

# Agent / fixture-provider / Kiro protocol integration

This draft uses **test-build-only** fixture registration. The entry point lives
in kiro_agent_fixture_export_test.go and does not exist in production builds.
Fixture provider/API names are unique per invocation and removed on test cleanup.
No Kiro provider is added to builtin registries; no default transport is called.

## Exercised chain

Public agent.NewEngine → actual default pigo model bridge → temporary fixture API
→ strict Kiro request builder → injected in-memory HTTP response/frame collector
→ completed tool queue → actual agent permissions/local counter-only fake tool
→ next-turn raw evidence/history → second fake text response → final agent state.

The fixture handles only an initial empty system message carrying tools. It rejects
a real system prompt/later system changes/hosted tools rather than lowering those
semantics. This restricted test adapter is NOT a production system-message policy.
Tool schemas in the test are plain maps serialized only by the test harness.

Tests verify:
- allow: one fake execution, two requests, exact 9007199254740993 and original raw
  argument text survive agent and host transcript; success result reaches next turn;
- permission denial and BeforeToolCall block: rejection hook reached once, no
  execution, error result reaches the next request, then final text succeeds;
- JSON snapshot restore reaches a third provider dispatch but fails actual strict
  Kiro history projection before any third HTTP call; no raw is reconstructed;
- truncated tool response and cancellation during fake exchange: one attempt,
  zero executions;
- temporary registry setup/cleanup does not invoke real auth, login or network.

Original nonempty raw is checked against the fixture source before serialization;
an independent observer also asserts exact nonempty raw on successful turn two
and empty raw after JSON restoration. Missing raw is deliberately passed onward
so the production strict projector,
not a harness shortcut, is responsible for rejecting restored history.

## Real defect found and minimal fix

The first actual agent run made zero HTTP calls. agent's tool conversion stores
map[string]any(nil) in the any-typed OutputSchema field. Kiro's prior !=nil check
misclassified that absence as an unsupported output schema.

The only production change accepts exactly nil or map[string]any(nil) as absent.
An allocated empty map, a populated schema and a nil pointer remain unsupported;
no declared output schema is dropped. A direct regression distinguishes those
forms. Existing generic agent conversion and other providers are unchanged.

## Still not production-complete

- No registered/default-enabled Kiro API/provider or public configuration contract.
  Fixture registration cannot be used outside go test.
- No login, browser/CLI token discovery, refresh, profile/account selection, real
  service entitlement/terms verification or charged inference validation.
- No persisted raw-evidence replay; JSON restore intentionally fails closed.
- System/developer instructions, dynamic tool/system updates, reasoning/signature
  replay and partial/context/metering usage remain restricted or unsupported.
- Combined response adapter is buffered; tool events are released only after final
  validation, not realtime tool argument execution.
- Agent execution still uses ordinary cancellation/schema/permission checks.
  Already completed Result is not revoked by later cancellation.

No external action, actual tool side effect, credential, billing or CI dispatch is
part of these fixtures. Tests are independent synthetic scenarios, not upstream
source copies or official Kiro captures.
