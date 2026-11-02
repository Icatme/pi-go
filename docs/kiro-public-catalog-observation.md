# Explicit public catalog observation

This draft exposes the already fake-tested management pipeline; it does not make
Kiro available by default or assert account entitlement. `FetchKiroModelCatalog`
requires a keyed `KiroCatalogFetchConfig` with explicit scope, origin, user agent,
byte/model limits, credential callback, executor and clock. Zero configuration
fails; registering a provider never invokes this operation. The supplied executor
is the only possible network boundary and must not retry or redirect. Tests use
only synthetic responses and credentials, not the real service.

The opaque returned `KiroModelCatalog` exposes `Models` and `Select`. Both require
matching asserted scope, an explicit current time and maximum age (positive, at
most 30 days); the exact age boundary is stale. `Select` requires either
`KiroCatalogAlias` or `KiroCatalogServiceID`: overlapping namespaces are never
guessed. The service ID is observed data, not an automatically routed host Model.

`KiroModelObservation` has no mutable public fields. Alias, service ID and display
name getters return values; optional token limits return `(int64, bool)` so absent
limits are not invented. Raw schema/metadata getters return independent copies.
Lists and selections are detached and may be read concurrently without sharing
caller-mutable buffers. Do not mutate callback-owned inputs concurrently with a
fetch; callback implementations and their cancellation behavior remain trusted.

Scope and clock are caller assertions, not authenticated account identity or
server time. Freshness does not prove continuing access. There is no implicit
cache, persistence, default selection, pricing, capability inference, model
registration, login, profile discovery or automatic refresh. Catalog/observation
JSON is `{}` and is not a restore format; fmt methods redact their content.
Explicit raw getters can reveal sensitive upstream metadata and must not be
indiscriminately logged. HTTP request headers contain credentials and the URL
contains the explicit profile; callbacks must not log these either.

The underlying attempt validates HTTP status, MIME/encoding, byte/model budgets,
known length, EOF, cancellation and successful Close before creating an
observation. Callbacks are cooperative: an executor/reader/Close that ignores
context cannot be forcibly interrupted. All public errors are fixed, without
raw response or credential text. This retains the strict community-derived
subset documented in the preceding catalog changes, not official API permission.

## Independently authored regression scenarios

External-package tests cover explicit callback counts and Close-before-clock,
unchanged provider registry, exact alias/service lookup, large int64 token limits,
absent limits, detached list/raw buffers, concurrent reads, fmt/JSON redaction,
scope/freshness rejection, nil/zero catalogs, zero config and late Close failure.
These extend the upstream v0.12.3 `dd93f2e` management/models scenario mapping;
no upstream implementation or fixture was copied. Real endpoint acceptance,
real account authorization and runtime model-ID routing remain unverified.

## Explicit caller mapping into a registered runtime

The registered Kiro module sends `Model.ID` as the exact service `modelId` for both
current and historical user messages. It does not resolve catalog aliases. A
caller may explicitly select an observation with an asserted matching scope and
freshness policy, then put `observation.ServiceID()` into its own `Model.ID` while
supplying its own API/provider identity and other host model policy. This neither
registers a catalog model automatically nor fills missing capability/price fields.
The returned host message then uses this same exact service ID, not an alias.
Do not copy `Alias()` into `Model.ID` expecting automatic translation.

A synthetic public-path integration test covers two entries whose alias/service
namespaces overlap (`sample-1.2.3` and `sample-1-2.3`). It checks explicit selection,
exact JSON paths in current/history requests, final result identity and one
catalog/one runtime attempt with no fetch on registration. This is manual caller
wiring, not a dynamic resolver, implicit freshness recheck at every dispatch,
automatic account policy, real endpoint acceptance or generic model factory.
