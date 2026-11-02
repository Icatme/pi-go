# Explicit credential source and remaining registration gates

Depends on #95. Adds an internal **unsent request** constructor, not provider
registration, login or a network operation.

## Credential source contract

Request/schema/region/identity validation and body serialization happen first.
Only then is the explicit source called once with a copied ProfileARN/Region
scope. It returns kind/token plus an exact matching scope. This prevents accidental
cross-scope reuse; it is an assertion by the trusted caller, NOT cryptographic
token ownership, account entitlement or service authorization.

Source errors, mismatched scope, malformed kind/token and cancellation return nil
request and a fixed error without source text or credentials. Failure closes the
prepared body. Successful caller owns request/body. No cache, global token state,
retry, refresh, environment/CLI discovery, persistence or automatic HTTP client.
A source must cooperate with context; arbitrary blocked source code is not
preempted and panics are not recovered.

Credentials are omitted from default encoding/json and common fmt output is
redacted. The source's Token field and successful request Authorization header
remain secret data in memory; do not log them. This is not zeroization or complete
secret-management protection. Existing AuthConfigs and APIKey options are not
automatically consulted.

Four independently authored tests cover explicit bearer/API-key/external-IDP
headers, exact scope, source-time input edits not changing body, invalid requests
never resolving, scope/error/cancel failure, concurrent independent resolution,
and token omission from body/URL/JSON/common fmt.

Community comparison is pinned to
[oauth.ts dd93f2e](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/oauth.ts).
That source stores region/profile metadata and uses bearer/API-key headers; its
CLI credential discovery, refresh/writeback and network model-cache update are
NOT ported here. Explicit callbacks are the safe injection seam for future
authorized implementations, not a claim of feature parity.

## Optional production registration: open decisions, not hidden defaults

A production factory should return modules/configuration without modifying global
registries until the caller explicitly registers them. It needs:
- explicit caller-selected API/provider/model identities and allowed model set;
- explicit request identity/profile/region, per-attempt IDs and credential source;
- required injected single-attempt transport, with no default-network fallback;
- bounded stream/queue settings and rejected unsupported semantics/options;
- teardown/ownership semantics and concurrency-safe per-attempt state.

Current StreamSimple unconditionally sets options.HTTPClient via providerHTTPClient
before the API callback. A future injected-executor API must define how an explicitly
supplied client/proxy differs from this host-created default; silently ignoring
custom transport policy is unacceptable. This draft deliberately does not solve
that through undocumented pointer comparisons or enable registration.

SessionID, sampling/max-token/tool-choice/parallel settings, payload/response hooks,
structured response format, observer, retry/timeout and reasoning options need an
explicit accept/map/reject matrix. The fixture's permissive option handling is not
a production contract. Credentials from generic APIKey/Auth versus the explicit
source must have unambiguous precedence; this constructor supports only the source.

## System prompt gap

Current request projection rejects SystemPrompt, and the successful agent fixture
only extracts an initial empty system/tool declaration. Upstream compatibility
approaches that flatten or prepend instructions do not preserve native system
priority automatically. Supporting that transformation requires explicit policy,
budget/escaping/order tests and clear documented semantic downgrade; do not silently
append system/developer text to a user message. Dynamic system/tool declarations
and reasoning replay remain separate gaps.

No real credentials/service calls/billing, provider enablement, login or deployment
occurred in this change.
