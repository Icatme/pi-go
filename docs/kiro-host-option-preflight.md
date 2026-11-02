# Host option preflight and explicit registration preparation

Depends on #97. No Kiro provider/API is registered by this change.

## Optional generic API hooks

APIModule's exported field layout remains unchanged, including positional literal
source compatibility. A new RegisterAPIModuleWithValidationForSource entry point
accepts a separate APIOptionValidation value. Module and validation live in one
internal registry record and are resolved as one generation snapshot. Existing
GetAPIModule pointer access remains mutable by convention; this is not deep
immutability or protection against callers mutating returned modules.
If supplied, the relevant hook runs before host HTTPClient wrapping and before the stream callback. Full
Stream validates after its existing provider NormalizeOptions step, so it sees
effective normalized options; Simple sees its normal merged headers/options.
Existing response-format/tool-sampling validation still runs first.

Hook failure creates the existing error stream/result with fixed text
"api options rejected". Raw error text and option values are not exposed. No
observer, auth resolver or stream callback is invoked by these hooks. Hook code
is trusted and must be pure/read-only; this contract is not enforced by sandboxing,
does not recover hook panics, and is not execution/network permission.

With nil hooks, existing providers retain normal client wrapping, routing and
terminal behavior. Both Stream and StreamSimple are covered. Existing lazy registration also retains
its previous public interface. Unregister prevents future resolution, not already
captured in-flight requests; those retain both their original module and guard.
A separate loose lookup table is deliberately avoided so registry churn cannot
pair an old callback with a missing/new validator.

## Kiro strict subset (not installed automatically)

The two internal Kiro validators accept:
- a nonnil, noncancelled RequestContext;
- Transport empty or SSE as the host's non-WebSocket HTTP-stream selector.

SSE here does NOT claim textual server-sent-event wire format: Kiro remains AWS
binary event-stream. Auto and WebSocket selectors reject rather than being ignored.

Every other nonzero option rejects. This includes generic APIKey/Auth, custom
HTTPClient, headers, session ID, token/sampling/parallel settings, cache policy,
retry/timeout, hooks/observer, metadata, reasoning, response format and previous
response/truncation settings. Even allocated empty containers are nonzero at the
validator boundary. Host header merging can already normalize empty header maps.

After clearing only accepted fields on a value copy, reflect.Value.IsZero checks
the remaining struct, including future fields. It supports maps/functions/
interfaces without comparability panic and invokes no custom method/serializer.
No unsupported value is logged or included in errors.

A future explicitly registered Kiro module attaches these hooks, so user-provided
custom clients reject *before* the host installs its normal wrapper. It can then
use only its separately required injected executor/credential source. The generic
host may still construct its ordinary client object; construction is not a network
request and does not authorize using it. This change itself neither provides that
factory nor guarantees an arbitrary API callback won't use its client.

Directly invoking an APIModule callback can bypass host hooks, so these hooks are
an API dispatch contract, not an adversarial security boundary. Any future public
standalone Kiro entry point needs its own explicit validation contract.

## Tests and remaining work

Eight tests cover every unmapped option field (including map/function/interface/
pointer/struct), zero/nil/cancelled contexts, transport selection, no input mutation,
preflight-before-wrapping order, fixed error output, no-hook legacy routing, and
actual Kiro-hook discrimination between caller client and host default wrapping,
legacy positional APIModule literal compilation, deterministic in-flight generation
replacement, and concurrent registration/unregistration with denied dispatches.
All registered fixtures are test-only, cleaned up and never send HTTP requests.

Upstream plugin supports options beyond this subset. Rejecting them is deliberate
until their exact request/response semantics have independently migrated tests;
this is not full upstream feature parity.

Still required: explicit public module/config factory, per-request identity/scope
ownership, tool-schema source, credential/executor injection, caller-owned
registration/cleanup, system lowering diagnostics and supported option expansion.
Login/refresh/account/service verification and persisted raw replay remain absent.
No default enablement, live credential/service, CI, deployment or extra charge.
