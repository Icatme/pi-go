# Injected single-attempt boundary (not registered provider support)

This draft is based on #71 and adds a small execution seam over its constructor.
`runKiroInjectedAttempt` accepts an explicit function and calls it exactly once.
There is no default http.Client/Transport, automatic retry, credential refresh,
provider registration or implicit external call. Tests supply only local fake
functions and synthetic streams; no real service or credentials were used.

## Ownership and fail-closed rules

- Validate response budgets and construct a new explicit-credential request;
  reject nil executor, nil/cancelled context or invalid input before execution.
- Call the injected function once. Do not expose its raw error text (which may
  contain URLs/headers/secrets), or retry after an uncertain failure.
- Reject every status other than 200, including 3xx, without reading error bodies.
  No Location is followed. If a response accompanies an error, still close it.
- Require exactly one event-stream Content-Type, with a bounded header value;
  only absent/identity Content-Encoding is accepted. Reject Uncompressed=true
  too: Go transports can remove gzip headers after auto-decompression.
- Apply a total body-byte allowance plus one probe byte, then existing framed /
  payload/text/event/tool limits. Exhaustion cannot manufacture successful EOF.
  A declared nonnegative ContentLength must match the bytes actually consumed.
- A received nonnil body is always closed. Body-close error, cancellation during
  close, parse/terminal/byte-limit failure or cancellation clears all results.
  No partial tools/text become caller-visible before these checks complete.
- Incoming stream/header/transport error bodies are never included in errors or
  logs. Returned requests are not logged; credentials remain sensitive in memory.

Limits cap body wire at 16 MiB, frame/argument at 1 MiB, aggregate payload at
16 MiB, text at 4 MiB, events at 65536 and tools at 1024. These are conservative
client subset limits, not service maximums or hard process heap/time guarantees.
Error body read limit is zero: rejected HTTP responses are closed without read.

## Trusted injection and remaining responsibilities

The injected function must itself behave as one RoundTrip: honor request context,
not follow redirects/retry, and return a context-aware body. A wrapper around
http.Client.Do which follows redirects would violate this contract. This code
cannot prevent arbitrary injected code from making extra network calls, mutating
objects or ignoring cancellation. No goroutines are spawned to hide a blocked
custom function, Read or Close. There is no self-enforced wall-clock deadline.

The fixed constructor host/region syntax is not account/profile/token binding or
proof of service-use authorization. Proxy policy, approved region/account binding,
real endpoint permission, token lifecycle and spend scope remain separate gates.
No production client factory is supplied. This branch also does not include #70's
contextUsage/retry-policy integration, and does not select any automatic retry.

## Test sources and validation status

Fake-only cases cover exactly-one execution, explicit fake auth and endpoint,
response close, 307/308/401/429/503 rejection without reading, header contradictions,
unsupported/auto-decompressed encoding, response+error/nil response/nil body,
close failure, truncated streams, ContentLength mismatch, exact byte budget and
limit-generated EOF, pre/in/post-read/close cancellation, and nil executor/invalid
limits. Secret-shaped fixture markers never enter returned error messages.

Independent static review found the Uncompressed boundary; it was fixed with a
read-zero/close-once regression source. Go compilation, gofmt, unit/race tests
have NOT run because an approved usable Go compiler is unavailable. No compiler
installation, CI dispatch, real network, actual credential, master merge or
deployment occurred. Passing test source review is not a working Kiro provider.
