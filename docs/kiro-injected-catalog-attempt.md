# Single injected catalog attempt

This internal bridge connects the offline management request builder to the
scope-bound catalog observation using only caller-injected functions. It is not
called by provider registration and has no default HTTP client, redirect follow,
retry, profile probes, model registry update or cache. Tests use in-memory fake
executors/bodies/clock/credentials only; no actual management API is contacted.

The request's routing/scope/client/auth inputs are validated before one executor
call. Success requires HTTP200, a body, exactly one application/json Content-Type
(optionally UTF-8 charset), absent/identity encoding, no automatic decompression,
valid declared length and bounded read to actual EOF. Duplicate type/encoding
headers, unknown charset/media parameters, malformed JSON/catalog and API errors
fail closed. A maxBytes+1 probe distinguishes budget EOF from real EOF. Existing
context-aware sticky/non-progress reader prevents swallowed read errors or endless
(0,nil) loops; it cannot preempt a blocking trusted Read.

Every returned response body is closed, including transport-error+response and
rejected status/header cases. Close failure or cancellation clears all observation
evidence. Only after successful Close does the explicit clock callback run; its
error/zero time/cancellation also rejects. Constructor binds the result to the
original request scope. The observation timestamp is caller asserted, not server
provenance, and freshness/account entitlement are not inferred from HTTP success.

Source, executor, reader, Close and clock must be concurrency-safe/context-
cooperative as appropriate; the bridge cannot prevent a supplied executor from
violating its no-redirect/no-retry contract. No function is installed automatically.
Do not log token headers, profile URLs or raw response metadata. Errors are fixed
and do not expose transport/body/clock messages. JSON/UTF8 MIME restriction is a
local conservative boundary and may reject service responses not covered by tests.

## Independently authored scenario coverage

Pinned community baseline dd93f2e management.ts and management.test.ts supply the
catalog HTTP/response intent. The Go ownership, budget, cancellation and Close
cases are independent hardening, not copied upstream tests or claims of parity.

- Validated GET→blocked Close→explicit clock→scoped exact model selection, with
  large integer metadata and one source/executor/body-close lifecycle.
- Transport+response, 302/401, encoding/charset/duplicate header, length/budget,
  read/no-progress, Close/cancel, invalid JSON/empty catalog reject without retry.
- Preflight before source/executor, failing/zero clock, exact-budget EOF, complete
  bytes accompanied by error or wrapped EOF, nil response/body and decompression.

No live endpoint/client acceptance, token ownership, subscription availability or
billing is verified; this only closes the injected fake-response pipeline.
