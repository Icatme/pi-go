# Caller-owned desktop refresh API

`RefreshKiroDesktop(ctx, KiroDesktopRefreshConfig{...})` exposes the single injected
attempt. Use keyed fields with explicit scope, user agent, refresh token, byte
limits, lifetime/skew, clock and executor. There is no default network/client,
desktop app or CLI launch, browser login, credential discovery, timer, cache,
source mutation, registration or persistence. Zero configuration fails.

The only possible request goes through the injected executor, once. The endpoint
is pinned community-observed behavior, not a verified official public API promise.
The caller must already have authorization for any real credentials/service use;
synthetic tests do not establish it. No real calls or credentials were used here.
The explicit UA is not replaced with a default desktop impersonation.

A successful opaque `KiroDesktopRefreshResult` exposes copied Credential(),
RefreshToken(), ExpiresAt(), RefreshAfter() and RetainedRefreshToken(). A genuinely
missing rotation field retains the exact request token only on fully validated
success; null/empty/error does not. Explicit returned token wins. The provenance
flag does not promise continuing token validity. A returned profile must match
the caller's asserted profile; absence or matching strings are not identity proof.

Clock is sampled before dispatch and after successful Close. Deadline is anchored
conservatively to the first observation; backward/invalid/expired end observations
reject without extending expiry. Both clock readings must be trustworthy and
consistent. RefreshAfter may already be due. Result delivery waits for complete
HTTP/length/MIME/body/Close/clock/parser validation.

`errors.As` to `*KiroDesktopRefreshError` exposes ExecutorInvoked(). False means
this helper did not invoke the executor; true means control was handed over, not
proof of bytes sent/receipt. A server may rotate tokens despite lost response,
profile rejection, failed Close/parse or expiry. There is no fallback result or
implicit retry, and old-token validity cannot be inferred from any failure.

The executor must not redirect, retry or log/persist secret bodies. Hidden callback
behavior cannot be forcibly constrained; executor/Read/Close/clock cancellation
is cooperative. Plaintext strings and buffers are not securely zeroized. Explicit
result getters contain secrets and must not be logged. Opaque result/error JSON
is `{}`; callback config is not JSON-serializable. fmt forms redact secret values.
No store or source lock is installed. Caller must serialize a credential generation
across all consumers/processes and fence late results before any source update.

Three original external-package tests cover exact fake requests, retention and
rotation, asserted scope and conservative dates, callback/Close counts, copied
getters, zero values, fmt/JSON, errors.As and pre/post-dispatch failures (including
response loss, profile/null/Close/clock/expiry/redirect). They are not a real-world
login/refresh/account test. See [attempt contract](kiro-injected-desktop-refresh.md)
and [request sources](kiro-offline-desktop-refresh-request.md).
