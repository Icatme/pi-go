# Kiro development-stack convergence review — 2026-11-02 UTC

## What this snapshot is

This is a tested **development stack**, not a released/default-enabled provider.
The final handoff increment adds tests/docs only. All publication in this work was
to draft branches; master was not merged or deployed, and no CI was dispatched.
Official Go1.26.2 in the authorized cloud workspace used cached dependencies and
live flags off. Final per-increment results are in the worklog. Independent reviews
were static, not independently executed tests. Real services/accounts were not used.

At 06:43 UTC a read-only GitHub check still found upstream main at
[dd93f2e](https://github.com/mikeyobrien/pi-provider-kiro/commit/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba),
committed2026-10-29T22:21:36Z, release message v0.12.3. No new main delta relative to
the pinned implementation evidence. The earlier tag-list endpoint was unavailable;
this does not claim every branch/release/tag was inspected.

## Implemented and connected, with limits

| Surface | Actual evidence | Still not established |
| --- | --- | --- |
| Request/auth/client metadata | Scoped explicit credentials, caller origin/UA, bounded wire JSON, explicit auth kinds, fake request tests | Real server acceptance, TLS/client fingerprint parity, permission to impersonate any client |
| Runtime/Agent | Explicit buffered, live-text and mixed-live registrations; Auto/session opt-ins; multi-turn tool permission/cancel/raw-argument tests | Default installation, live credentials, production entitlement/performance |
| Live behavior | Pipe fixtures deliver provisional text before terminal; tools withheld until full validated terminal/HTTP completion | Real-stream scheduling/performance and complete upstream event parity |
| Model catalog | Explicit injected fetch, immutable scope/freshness observations, explicit ID namespace; manual exact service-ID runtime regression | Automatic host model registration, inferred capability/pricing, continuing account access |
| IDC refresh | Explicit public one-shot caller config; opaque result, unknown-rotation error marker; optional successful-omission retention | Device/browser login, client registration, automatic source cache/update/store |
| Desktop refresh | Explicit public one-shot community-observed request; profile matching, omission provenance, conservative clock semantics | Official public API guarantee, actual desktop credentials, real login/refresh validation |
| Refresh-to-runtime wiring | Both public refresh APIs -> caller expiry-check source -> registered provider fake; wrong scope/expiry suppress runtime, no hidden refresh | A production credential manager or same-token concurrent refresh safety |
| Secret tests | Fixed errors/opaque formatting, explicit getters; no refresh/client secret in runtime URL/body/headers; access only Authorization | Secure memory erasure or behavior hidden inside caller callbacks |
| Regression found by audit | Close latency previously extended refresh expiry; failed-old-tree reproducer then fixed conservative pre-dispatch anchor | Accurate server issue time or resilience against dishonest/discontinuous caller clock |

## Explicit remaining gaps

- **No default network executor.** Callers supply a trusted callback prohibiting
  redirects/retries/logging and honoring cancellation. Arbitrary callback behavior
  cannot be forced by these helpers. There is no live end-to-end service proof.
- **Credential generation is caller-owned.** Serialize refresh for a generation
  across consumers/processes, fence late results and treat executor-invoked failure
  as unknown rotation. No persistence or silent CLI/IDE import was introduced.
- **External IdP** has a strict offline response component, not a public arbitrary-
  URL sender. A later implementation needs explicit endpoint trust/allowlist,
  redirect policy and ownership rather than accepting token-controlled URLs.
- **Protocol parity remains incomplete:** reasoning/signature replay, richer image
  history, automatic effort/schema policies, metering credits, some upstream EOF
  heuristics and permissive envelope variants are not silently accepted.
- **Persisted tool-session replay** cannot reconstruct original numeric wire
  evidence from float maps; exact in-memory evidence exists, restored raw evidence
  still requires a separately designed trusted provenance/serialization contract.
- **OAuth extensions:** unknown fields and local token-size limits remain a strict
  subset, not generic RFC-compliant extension handling. Optional IDC rotation is
  now explicitly supported, but only on complete validated success.

## Converge before expanding

1. Review the integrated branch against the unchanged base rather than treating
   each small PR as a separately usable product. Focus on public option contracts,
   registry changes, host/Agent terminal semantics and all credential lifecycles.
2. Decide the supported subset and release/API compatibility policy; do not label
   the entire upstream test corpus migrated or the provider production-ready.
3. Validate the supplied executor independently in a sandbox (redirect/timeout/
   cancellation/body/log policies) before any separately authorized real-service
   smoke test. That authorization was not inferred from development permission.
4. Only then select a concrete missing feature from the above list. Do not add
   unconnected APIs or automatic long-lived source management merely to grow the
   change list. No master merge/release/deployment is part of this review.

See [refresh audit](kiro-refresh-audit.md),
[caller-owned runtime handoff](kiro-refresh-runtime-handoff.md),
[status chronology](kiro-followup-status.md) and [actual test records](PO_AGENT_WORKLOG.MD).
