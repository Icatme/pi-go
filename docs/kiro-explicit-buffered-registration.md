# Explicit buffered Kiro API registration (community protocol)

Depends on #100. `RegisterKiroBufferedModule(sourceID, KiroBufferedConfig)` is an
explicit opt-in API registration entry point. Importing the package does nothing.
Registration validates scalar configuration and copies/validates the schema catalog;
it invokes no identity, credential or transport callback. Existing duplicate-API
registration panic semantics remain. Use a unique source/API and call
`UnregisterAPIModules(sourceID)` when done. In-flight requests retain their captured
module; unregister is not cancellation. No provider/model catalog is registered.

## Required caller ownership

- Explicit API and Provider routing names, region, profile ARN, origin and user agent.
  No CLI identity is selected implicitly; syntactic acceptance is not service-use
  permission or proof that the service accepts a client identity.
- `RequestIDs(ctx)` supplies conversation affinity and per-attempt invocation ID.
  No ID generation/persistence/cache is supplied. This trusted callback executes
  before context preparation; therefore it can run even for a later-rejected
  conversation. Any caller callback side effect is not rolled back. It must be
  read-only, concurrency-safe and honor ctx.
- `Credentials(ctx, scope)` supplies an explicit supported credential kind/token.
  Called once only after input validation and request serialization. Scope must
  match exact configured region/profile, but is not cryptographic account proof.
- `RoundTrip(request)` is the sole transport. It must honor context, not follow
  redirects/retry internally, and return a context-aware body. The adapter cannot
  forcibly interrupt an uncooperative callback/Read/Close. No default client.
- A raw schema catalog for possible tools. Selected schemas must exactly match
  each declared tool's plain Parameters map; catalog membership alone is not tool
  authorization. Tool execution remains entirely in the host agent permission path.

Callbacks contain secrets and may capture state. The adapter does not log or cache
credentials; returned HTTP headers necessarily contain them. Never log requests.
Caller callback safety and closure synchronization remain caller responsibilities.

## Dispatch contract

Use normal `Stream`/`StreamSimple` with matching Model.API/Provider and a nonnil,
active RequestContext. Optional transport may be empty/SSE (the host's HTTP-stream
selector, NOT the binary wire encoding). All other nonzero request options reject
before callbacks, including APIKey/Auth/custom HTTPClient/headers/observers/retry/
reasoning/sampling. Model BaseURL/Compat/headers/sampling or thinking maps also
reject; model identity/capability/cost metadata is not account entitlement or billed
usage. No model parameters are silently mapped to unsupported service options.

Only authoring context or one static initial system record is supported. System
instructions reject by default. Explicit `SystemPromptPolicy: "prepend_first_user"`
is a role downgrade, not native system/developer support; it is not automatically
reported as a new response flag. Dynamic system/tool updates remain unsupported.

This module is named **buffered** deliberately: no provisional text or executable
tool events are published before the entire HTTP response, EOF/length/Close,
terminal metadata and final projection pass. Successful complete lifecycle events
are then delivered to the normal host queue. It does not connect the separately
implemented realtime text-only observer. Partial token usage, reasoning/signature replay and unsupported event shapes still
fail closed. Valid context percentages are retained only as final Diagnostics,
never token/cost usage; see kiro-completed-context-diagnostics.md.

The public registry still exposes GetAPIModule for historical compatibility.
Calling a module's raw callbacks directly bypasses host option hooks. That is not
a supported dispatch path or an adversarial sandbox; use Stream/StreamSimple.

## Fixed bounded subset

Request: 4096 messages, 128 tools/history calls, 1 MiB decoded payload and serialized
body. Catalog: 128 entries / aggregate 1 MiB. Response: 16 MiB wire, 1 MiB per frame,
4 MiB aggregate payload, 1 MiB text, 16384 events, 128 tools, 1 MiB per argument.
Final projection: 1 MiB and existing host event byte budget. These are validation
limits, not hard process-memory guarantees; smaller caller limits are not yet exposed.

## Verification / remaining

Fake tests cover full/simple public dispatch, registration without callback calls,
unsupported options/input rejection, two-round raw tool replay, catalog isolation,
unregister, invalid configuration, incomplete/cancelled/Close-failed/unsolicited
tool responses emitting no tool or success events, and external-package API
compilation. No tool is
executed by these tests and no real endpoint/credential is used. Existing migrated
protocol/history/request scenarios remain in the test stack.

Not claimed: official private endpoint support, real account/model availability,
login/refresh, persistent raw replay, realtime text/tool parity, automatic catalog
installation, or deployment. An explicit adapter exists; production service
interoperability still needs separately authorized verification.

## Later opt-in Auto selector

The original entry remains strict. `kiro-explicit-auto-http.md` documents a
separate controlled-options entry that explicitly chooses the injected HTTP path
for Auto, without WebSocket attempts/retries or changes to this config layout.
