# Kiro offline request envelope and next integration gates

## Delivered in this draft

`encodeKiroOfflineRequest` composes validated text/tool projections into the
community-observed `conversationState` / `currentMessage.userInputMessage` /
`history` envelope with `MANUAL`, `vibe`, explicit conversation ID and profile.
The upstream schema reference is pi-provider-kiro commit
`dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba`, `src/stream.ts` lines 990–1008,
and `src/transform.ts` lines 34–44, 227–233 and 338–345.
This independently written code does not copy upstream implementation.

Origin is supplied explicitly and applied to both current and historical user
entries; there is **no default KIRO_CLI, AmazonQ or SDK identity**. The fixture
uses PI_GO_FIXTURE, whose acceptance by the service is NOT established. Identity
strings are bounded and syntactically screened, not authenticated: profile ARN
is opaque here, with no claimed account ownership, region or permission check.
No conversation identifier is generated or reused automatically.

Only already-validated DTOs are serialized, never arbitrary Context or tool
Parameters. System prompts are rejected rather than silently lowered; images,
additionalModelRequestFields and signed reasoning replay are not yet supported.
Original raw tool JSON precision and empty tool-result carriers are preserved.
Existing strict history and raw-evidence restrictions remain: ordinary persisted
history is NOT supported, and no parsed map is quietly cleared/reconstructed.

Inputs must not mutate concurrently. Output bytes are owned by the caller and
have no aliases to schema buffers. All failures return nil bytes, no truncation
or partial request. Limits cap decoded payload at 1 MiB, messages at 4096 and
history calls at 1024, independently of larger caller requests. The Tools
limit applies to both catalog and calls; the underlying history validator
additionally caps the catalog at 256. A nonempty ProfileARN is a local subset
requirement, not a claim that all upstream authentication paths require one.
Final serialized
bytes include escaping, duplicated/catalog data and envelope overhead. The
wire-size limit is checked after json.Marshal and **is not a hard heap limit**;
transient escaped output and intermediate structures can exceed that limit.

## Verification

Go test sources cover fixed complete text-envelope shape, empty history omission,
explicit identity, tool-call/result pairing and exact 9007199254740993 evidence,
input/output ownership, malformed configuration, unsupported transcript failure,
and exact serialized-byte boundaries including JSON escaping expansion.
Go compilation, gofmt, unit/race tests have NOT run: `/usr/bin/go` in the current
cloud environment is not the Go compiler and no installation is authorized.
This draft depends on #61 and has not been merged into master or deployed.

## Remaining work, split by independently reviewable PR

1. **Compile/test gate:** obtain an authorized existing compatible Go toolchain;
   run TestKiroRequestEnvelope, TestKiroOfflineIntegration, all TestKiro, race
   and full suite; format/review before considering any merge.
2. **Projection parity:** pure-text ContentBlock user input in tool conversations;
   images and additional model fields only with explicit validated schemas.
3. **Client identity/header contract:** inventory upstream's KIRO_CLI origin,
   AmazonQ/SDK UA, invocation/request/opt-out/agent-mode headers; separate truthful
   client identity configuration from intentional compatibility simulation.
   No silent impersonation or claim that wire compatibility grants service use.
4. **Explicit authentication DTO:** caller-provided credential kind and account /
   region binding, secret redaction and lifecycle; do not discover CLI tokens or
   refresh credentials by default. Offline fixture validation before live scope.
5. **Injectable HTTP provider:** bounded streaming transport, cancellation,
   controlled endpoint selection and redirect policy, errors, retry/usage
   boundaries. Start with deterministic fake RoundTripper tests: no real network.
   Provider registration requires the adapter/error/terminal contract tests.
6. **Multi-turn evidence:** define trustworthy raw argument provenance across
   production provider conversion and persistence, edit invalidation and version
   handling. Never regenerate exact evidence from floating-point argument maps.
7. **Response compatibility:** typed context usage separate from tokens/credits,
   signed reasoning provenance/replay, documented unsupported events. Do not
   soften terminal-evidence/error rules to pretend broader compatibility.
8. **Real-service verification:** separate authorization, service-use contract,
   credentials/account and spending scope are required. Offline builders, UA
   compatibility and a CLI API key are not proof of permitted third-party calls.

Client simulation and HTTP integration are tracked gaps, not silently removed
from the plan. Their implementation, offline validation and live verification
must be reported as different states. This draft makes no HTTP request, reads
no credentials, registers no provider and does not trigger CI or deploy.
