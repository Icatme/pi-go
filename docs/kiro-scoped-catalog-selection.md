# Scoped offline catalog selection

This internal layer binds the existing offline model-catalog parser to an explicit
caller-supplied profile/region and observation timestamp. It is not a fetcher,
cache, account entitlement verifier, model registry or automatic provider setup.
The stored snapshot is private/owned and immutable; selection returns detached
raw metadata/schema and token-limit pointers. Concurrent selections do not mutate
shared state. fmt/JSON forms do not expose the stored profile or raw metadata;
returned individual observations still contain raw data and must not be logged
indiscriminately.

Selection requires exact matching scope, explicit current time and positive
maxAge bounded to 30 days (local admission policy, not an official refresh SLA).
Future observations and stale observations reject; exact maxAge is stale. No
implicit clock, default/static model, region probe or cross-account fallback.
Scope and observation time remain assertions from the caller; this is not
cryptographic provenance or proof of current account availability.

## Alias and service identity are separate namespaces

The prior parser preserves exact service IDs and community numeric-dot aliases.
Selection now requires an explicit `alias` or `service_id` mode. This avoids a
concrete ambiguous lookup pattern without changing upstream normalization:

- service `sample-1.2.3` produces alias `sample-1-2.3`;
- a different service `sample-1-2.3` produces alias `sample-1-2-3`.

Each alias is unique, but the first alias equals the second exact service ID.
A lookup string alone cannot safely choose a record. The explicit namespace is
required; selected ServiceID remains unchanged and must be the eventual runtime
request authority. No host Model, capabilities, prices or absent token limits
are invented. This does not yet connect service-ID routing to registered adapters.

## Original scenario mapping and tests

Baseline [management.ts](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/management.ts#L335-L376)
scopes model discovery to a profile and its region; [models.ts](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/models.ts)
uses normalized aliases while retaining service IDs. Existing catalog tests map
the response-data scenarios from upstream management/models suites. The explicit
namespace, freshness and ownership cases here are independently designed Go
hardening, not claims that upstream has identical tests or policies.

Four test functions cover overlapping namespaces, exact service-ID preservation,
scope/clock/freshness rejection with no fallback, nil optional limits, 16 concurrent
selections with caller mutation, exact9007199254740993 token limits, diagnostic
redaction and invalid/colliding catalog rejection. No network, real account,
credentials, registry mutation or management cache is used.
