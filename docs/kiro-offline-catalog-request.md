# Offline ListAvailableModels request

`newKiroOfflineCatalogRequest` creates an HTTP request but never sends it. The
caller explicitly supplies region/profile scope, origin, User-Agent and a scoped
credential source. There is no HTTP client/executor parameter, profile discovery,
region fallback, cache, registry mutation, retry, login or credential persistence.
A source is trusted/context-cooperative and could have caller-defined side effects;
only local synthetic sources are used in tests.

## Pinned wire evidence

Community [management.ts request assembly](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/management.ts#L107-L128)
and [ListAvailableModels](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/management.ts#L335-L354)
use GET at `management.<region>.kiro.dev/List-Available-Models`, with origin and
profileArn query parameters and JSON Accept. This adapter preserves that shape,
using explicit authentication kinds rather than unsigned JWT/token discovery.
It does not copy runtime invocation/retry/agent-mode headers or POST target headers.
User-Agent is explicitly supplied and validated, not silently chosen as a CLI.

Query/identity/context validation and request construction happen before exactly
one source callback. Returned credentials must assert the same scope. Callback
errors, cancellation, mismatched scope and invalid token syntax return a fixed
error with no request. A callback changing its captured options cannot reroute the
already prepared value-copy request. This is accidental-mixup defense, not proof
that a token belongs to the stated account/profile/region.

Returned headers contain a secret and URL contains a profile identifier: do not
log, dump or send the request to any other endpoint. No memory zeroization promise
is made. Bearer/API_KEY/EXTERNAL_IDP header forms follow the existing explicit auth
helper; API-key canonical-region discovery/rerouting from upstream is deliberately
not implemented. The chosen scope is caller-owned, and this builder alone does not
prove that any region or client identity is accepted by the real management API.

Three independently authored test functions cover method/path/query escaping and
all explicit auth families, absence of POST/runtime headers, rejection before
source, scope/cancellation/source failures, fixed secret-free errors and source
mutation isolation. Existing client-header regressions also run after extracting
its unchanged User-Agent validator for reuse. No upstream tests were copied and
no service/account/model availability was checked.
