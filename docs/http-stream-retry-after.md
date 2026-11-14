# Opt-in Retry-After for the low-level HTTP stream client

`HTTPStreamClient.RespectRetryAfter` defaults to false. Set it true when calling this exported low-level client's `PostStream` to respect standard `Retry-After` on an HTTP response that the existing policy already permits retrying. This does not add retry eligibility or attempts and does not govern network errors, SSE mid-stream failures, or redirects processed inside `http.Client`.

`RetryAfterMaxDelay` is a separate finite budget for a header-informed wait. Values <= 0 select 30 seconds. For a valid header the delay is the larger of the existing backoff (including its existing `MaxRetryDelay` cap) and the server delay. If that wait exceeds the new budget, the original HTTP error is returned without an earlier retry. `MaxRetryDelay=0` retains its existing no-cap meaning for backoff, but it cannot disable the new finite header-informed budget. Missing headers use the existing wait unchanged, even when the new budget is smaller.

One header value is accepted, up to 128 bytes, with surrounding space/tab ignored. Nonnegative decimal integer seconds and HTTP dates recognized by Go's `http.ParseTime` are supported. Past dates and zero seconds contribute zero; existing backoff still applies. Duplicate values, empty/malformed/oversized headers, integer overflow and over-budget dates stop the retry and retain the original HTTP error. Only standard `Retry-After` is parsed, not provider-specific `retry-after-ms`. Cancellation during the wait returns the existing context error contract. A rejected hint never enters the wait and leaves the existing original-HTTP-error construction in control, even if cancellation has already occurred; response-body read errors retain their existing behavior. Subsequent requests still carry the same context. Waiting is measured after the error response has been consumed; this can be conservative for seconds hints.

Example:

```go
client := pigo.HTTPStreamClient{
    MaxRetries: 1,
    BaseRetryDelay: time.Second,
    RespectRetryAfter: true,
    RetryAfterMaxDelay: 10 * time.Second,
}
```

This release intentionally does **not** expose the policy through `Stream`, `StreamSimple`, provider options, or the agent. Provider adapters that construct their own client retain default behavior; ordinary Responses, CommandCode, Kiro and other independent retry loops are not changed. No claim of global provider support or automatic agent-level recovery is made.

RFC 9110 section 10.2.3 describes the server waiting recommendation; it does not grant permission to retry non-idempotent work. This opt-in is a new bounded policy, not a correction to a pre-existing public option promise. Tests use local synthetic responses only.
