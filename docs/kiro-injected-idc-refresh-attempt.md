# Injected IDC refresh attempt (fake-tested only)

This internal increment joins the offline IDC request constructor to the strict
refresh response parser. It does not supply a network client, discover credentials,
register/login a client, update a credential source, persist a token, schedule
refresh or retry an attempt. All tests use synthetic secrets and injected fakes.

The caller explicitly supplies request inputs, asserted profile/region scope,
response byte limit, whole-second maximum lifetime, refresh skew and clock. Region
must match the request; preflight rejects invalid policy before invoking the
executor. The executor is called once. HTTP200, JSON MIME/encoding, bounded body,
exact known length, complete read and successful Close are required before the
ending clock and parser run. A starting clock is captured after request validation
and before invoking the executor. A close error/cancellation/error response yields the zero result
and a fixed error, never partial rotated credentials. Redirect responses are
rejected without reading/following their body. Response+error bodies are closed.

The implementation reuses the existing JSON-transport-only catalog response
reader; it does not invoke catalog parsing or model selection. No new permissive
JSON/length/EOF parser was introduced. The returned credentials retain the
caller-asserted scope; a refresh response cannot prove profile/account ownership.

The injected executor MUST NOT redirect, retry, log secrets or persist them. This
function can count its own invocation and reject returned 3xx statuses, but cannot
prevent arbitrary behavior hidden inside the callback. Executor/reader/Close/clock
must cooperate with cancellation; they cannot be forcibly interrupted. Never log
or dump the request/body, response payload or returned secret fields. Input/result
fmt redaction is not memory zeroization; plaintext strings/buffers remain in
memory until reclaimed, and an explicit consumer can read them.

The strict IDC parser requires an explicit nonempty rotated refreshToken. AWS's
CreateToken response describes that field as optional, so this conservative subset
may reject a valid real response. The attempt does not infer retention, extend
expiry or retry on that rejection. Unknown response fields likewise fail closed.
This compatibility limitation is deliberate pending separately reviewed semantics,
not evidence that an account/service violated a protocol. Real endpoint/client/
token validity and authentication integration remain unverified.

Sources and request safety constraints are recorded in
[the IDC request contract](kiro-offline-idc-refresh-request.md) and
[the response contract](kiro-offline-refresh-response.md). Original synthetic
regressions cover exact request input, caller-variable mutation isolation, close-
before-ending-clock/result, rotation/expiry, response+transport error, redirect/401/MIME/
encoding/length/budget/read/Close/cancel, missing rotation/invalid JSON/clock, and
preflight preventing executor invocation. This is neither official Kiro provider
support nor permission to use real accounts or billable services.


## Conservative expiry anchor (audit correction)

RFC 6749 section 5.1 defines lifetime relative to response generation, not local
body Close. The first implementation used a post-Close timestamp and could extend
a token deadline by transport/Close latency. The attempt now calls the SAME clock
before executor and after successful Close, validates both and rejects a backward
ending value. Expiry is computed from the request-start timestamp, conservatively
possibly earlier than the actual server expiry. If the ending value is at or past
that deadline, the attempt fails with an indeterminate rotation outcome.

Clock is now called up to twice, not once: initial invalid clock is pre-dispatch;
a final invalid clock/expiry is post-dispatch. Results remain withheld until all
checks finish. RefreshAfter may already be in the past, meaning refresh is due;
it must not move the expiry forward. This relies on a trustworthy, consistent
caller time source; it does not repair fabricated or discontinuous clock data.
Source: [RFC 6749 section 5.1](https://www.rfc-editor.org/rfc/rfc6749#section-5.1).
