# Explicit caller-owned IDC refresh API

`RefreshKiroIDC(ctx, KiroIDCRefreshConfig{...})` is an explicit operation, not an
automatic provider hook. Use keyed config fields with caller-owned scope, client
ID/secret, refresh token, user agent, request/response byte limits, lifetime/skew,
clock and executor. All are explicit: zero config has no working defaults. The
executor is invoked once at most. No client registration, login, credential
search, source update, cache, timer, retry, persistent access or provider/model
registration is performed. Tests use synthetic credentials and fake executors.

Success returns an opaque `KiroIDCRefreshResult` only after HTTP/body/Close/clock/
strict-parser validation. `Credential()` and `RefreshToken()` are explicit secret
getters; `ExpiresAt()` and `RefreshAfter()` retain separate expiry semantics.
The same Clock is called before dispatch and after successful Close (up to twice).
Expiry uses the pre-dispatch timestamp as a conservative local deadline, not the
post-Close time; backward/expired ending observations fail. A starting-clock error
leaves ExecutorInvoked=false; an ending-clock/expiry error leaves it true.
RefreshAfter may already be due at completion. The clock must be trustworthy and
consistent; this policy is not proof of the exact server expiration time.
Values are copied and no mutable token store is shared. Result JSON is `{}` and
fmt is redacted: this is deliberately not a persistence/restore format. Do not
log explicit secret getters. The config's three credential fields are excluded
from JSON and its fmt is redacted, but the actual request/response bodies contain
secrets in memory. No secure zeroization guarantee is provided.

## Failed rotation is not proof that nothing happened

Use `errors.As(err, &failure)` with `var failure *pigo.KiroIDCRefreshError` and
inspect `failure.ExecutorInvoked()`:

- **false**: this helper did not invoke the configured executor. It does not make
  claims about external actors or caller activity.
- **true**: control was handed to the executor. It may have sent zero, partial or
  all request bytes, and the server may already have rotated the refresh token.
  Lost responses, rejected JSON, failed Close and failed clock all remain
  indeterminate server-side outcomes. No returned credentials are exposed on
  those failures, and the old refresh token is not proven still usable.

This is intentionally NOT a "sent" flag, HTTP receipt proof, token validity proof
or retry-safe classification. Do not automatically retry either outcome without
a separate caller policy; particularly do not reuse an old token after an
indeterminate rotation expecting this package to recover it. No raw callback,
server body, URL or token error is retained in the public error.

## Trust and compatibility

The executor must never retry, follow redirects or log/persist secret bodies.
Returned 3xx statuses are rejected, but arbitrary hidden callback behavior cannot
be enforced. Callback/Read/Close/clock cancellation is cooperative. There is no
default network client. Real credential use and real calls require separately
explicit authority; this API's presence is not service-use authorization.

Scope is caller-asserted and not account ownership proof. This is IDC standard-
partition only; no desktop/external-IDP flow or region fallback is introduced.
The default strict entry requires nonempty returned rotation even though the
AWS field is optional. Omitting it fails with ExecutorInvoked=true, not an automatic
retention/retry. Unknown response fields also fail. Real service compatibility is
not verified. See [injected attempt](kiro-injected-idc-refresh-attempt.md) and
[request evidence](kiro-offline-idc-refresh-request.md).

Three original external-package test functions verify success/getters/copies,
no config mutation/persistence, exact fake request and Close, opaque formatting,
preflight nil/zero/cancel rejection and executor outcomes before body read, after
partial/full read with response loss, after Close/parse/clock failures and redirect.
These synthetic stages intentionally produce the SAME unknown flag once the
executor is invoked; consuming the body is not proof of a real service send.
The callback-bearing config itself is not JSON-serializable; serialization fails
rather than producing a credential configuration file. The typed public error
also serializes to `{}`; use its method, not JSON persistence, for outcome checks.


For successful responses that genuinely omit rotation, a separate explicit
[retention option](kiro-idc-refresh-retention.md) is now available through
RefreshKiroIDCWithOptions. The original entry/no-options remain strict. This never
rescues failed or indeterminate attempts and does not manage concurrent sources.
