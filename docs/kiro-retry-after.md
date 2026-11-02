# Offline standard Retry-After parser

Stacked on #64, this adds a separate unregistered pure parser. It does not call
the failure policy, schedule a wait, retry an operation, access credentials or
perform HTTP. No production behavior changes.

## Evidence and intentionally small scope

[RFC 9110 section 10.2.3](https://www.rfc-editor.org/rfc/rfc9110.html#section-10.2.3)
defines Retry-After as an HTTP date or nonnegative decimal integer delay-seconds.
The caller supplies a receipt timestamp; the parser never reads the clock.

[Community retry.ts L68-109](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/retry.ts#L68-L109)
also checks retry-after-ms and x-ratelimit-reset-after, accepts wider JavaScript
numeric/date syntax, rounds milliseconds and caps scheduled waits. We do not
port these broader behaviors. This independent implementation only accepts the
standard field with integer seconds or canonical IMF-fixdate. Obsolete HTTP
date forms and leap seconds are explicitly unsupported despite broader RFC recipient guidance;
accepted dates must be Go-representable and year 1900 or later;
this is a strict offline subset, NOT a fully RFC-compatible HTTP client.

Supply ALL raw values for this field. More than one value fails even if identical;
do not preselect one duplicate with Header.Get. A merged numeric list fails.
The raw field is limited to 128 bytes before trimming only SP/HTAB. Empty,
signed, fractional, exponential, non-ASCII numeric and malformed date values
fail. Date roundtrip rejects a wrong weekday or noncanonical timezone.

Present distinguishes absence from a valid zero/past-date hint. Date waits keep
subsecond remainder relative to caller receipt time. Negative maximum wait is
invalid. Integer comparison precedes duration multiplication; date comparison
precedes duration subtraction to avoid overflow/saturation. A value above the
caller's maximum returns a fixed safe error and zero result, NOT a shortened
wait. Errors do not contain raw header text.

## Integration boundaries

A parsed delay is NOT permission to retry, proof of idempotence, or assurance
that a request was unbilled. #64 still never authorizes replay of sent/unknown
dispatch requests. This parser has no fallback delay: invalid, ambiguous or
over-budget input must not silently become immediate retry. The future caller
must decide whether to stop, defer or expose the limit under its separate policy.

The maximum applies to this hint, not total deadlines, attempt counts or elapsed
time. This API does not enforce those. Clock skew and server Date calibration
are not solved; supply a trusted receipt time and retain separate deadline limits.
No transport/header extraction adapter is introduced.

## Verification status

Go test sources cover absence vs zero, exact integer/date values, fractional
remaining date waits, duplicate fields, malformed input, noncanonical/obsolete
dates, oversized fields, duration boundaries and budget rejection.
Independent static review only; Go compilation/gofmt/unit/race have NOT run.
No installation, CI dispatch, real service calls or credentials.

Suggested approved-toolchain checks:
- gofmt -w pkg/pigo/provider_kiro_retry_after*.go
- go test ./pkg/pigo -run TestKiroRetryAfter -count=1
- go test -race ./pkg/pigo
- go test ./...
