# Offline refresh-response contract

This is an internal pure parser for explicitly supplied successful-response JSON,
not a login/refresh implementation. It never reads CLI/IDE credentials, invokes a
credential source, chooses/calls an endpoint, changes an account, writes tokens,
starts timers, retries, or creates persistent access. HTTP status/authenticity
validation remains the responsibility of any future transport layer.

## Evidence and original test mapping

Pinned community baseline: [oauth implementation](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/oauth.ts#L361-L470)
and [oauth tests](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/test/oauth.test.ts#L33-L224).
Tests here are independently authored synthetic scenarios, not copied code.

- IDC camelCase response requires accessToken, refreshToken and expiresIn.
- Desktop camelCase response may omit refreshToken and retain the explicit old
  token; optional profileArn must exactly match the caller's scope.
- External-IDP snake_case response may omit refresh_token and retain the explicit
  old token. Its access token maps to explicit EXTERNAL_IDP auth kind.
- API keys do not have an OAuth refresh path and are rejected by this parser.

## Deliberate strict subset

Family, profile and region are caller assertions, not verified token ownership.
No family is inferred from a pipe-delimited secret or unsigned JWT. Mixed response
field families, unknown fields, changed profile, duplicate keys, invalid access
header syntax, empty/null refresh fields, invalid token type and invalid JSON
reject with a fixed error and zero result. Missing refresh fields differ from
explicit null/empty. This strict field allowlist may reject legitimate new upstream
extensions and is not a statement that those fields violate the real protocol.

The caller supplies clock, maximum lifetime and refresh skew. Lifetime policy is
positive whole seconds, bounded to 30 days; this is a local admission bound, NOT
an official service expiry limit. Response expiry must be a positive decimal JSON
integer within that bound, never string/fraction/exponent/default3600. Actual
ExpiresAt is now+lifetime. RefreshAfter is ExpiresAt-skew clamped to now. Short
lifetimes never gain extra validity, and no failure extends an old token's expiry.
The upstream's five-minute buffered timestamps/CLI fallback are intentionally not
copied. Region case normalization, profile discovery, IDE fallback, CLI SQLite
writes, refresh-token retry/rotation races and external endpoint discovery are
not implemented here.

Access and refresh tokens remain sensitive in memory. Result JSON excludes both
credential and refresh fields; fmt forms redact. This does not guarantee memory
zeroization or prevent callers explicitly logging raw strings. Input payload is
caller-owned/read-only; returned token strings are owned copies. No result is
installed into the registered provider automatically.

Four test functions cover family rotation/preservation and exact expiry, malformed
and unknown fields/overflow/duplicate keys, scope/clock/policy bounds and short
window behavior, plus JSON/fmt secret exclusion. All tokens are synthetic; no
real authentication or service acceptance is tested.
