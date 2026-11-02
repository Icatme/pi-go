# Explicit successful-omission retention policy

AWS CreateToken documents refreshToken as optional. RFC 6749 section 6 allows a
new token and requires replacement when one is returned. Together these support
an explicit policy retaining the exact request token when a successful response
omits that field; they do NOT establish continued validity after an unsuccessful
or indeterminate attempt. Sources:
[AWS CreateToken](https://docs.aws.amazon.com/singlesignon/latest/OIDCAPIReference/API_CreateToken.html),
[RFC 6749 section 6](https://www.rfc-editor.org/rfc/rfc6749#section-6).

`RefreshKiroIDC` remains strict and unchanged. The added
`RefreshKiroIDCWithOptions(ctx, config, WithKiroIDCRetainMissingRefreshToken())`
opts in without changing the exported config layout. With no options it is still
strict. Zero or duplicate options reject before clock/executor callbacks.

Retention happens only when all existing request/HTTP/MIME/length/read/Close/
clock/expiry/JSON checks succeed and refreshToken is genuinely absent. Explicit
null, empty, malformed or duplicate fields still fail. An explicit returned token
always wins. `RetainedRefreshToken()` reports the source of the successful result:
true for absence policy, false when a token was returned (even if identical).
This marker is provenance, not a promise the token remains valid or was authorized.

No error is converted into a retained result. After an executor-invoked failure,
rotation can be indeterminate and the old token must not be assumed usable.
There is no retry, fallback, disk write or automatic credential-source mutation.
The new internal policy path rejects use with other auth families; their original
parser behavior is untouched. Unknown response fields still fail, so this remains
an incomplete conservative OAuth/IDC subset, not full standard parity.

## Caller-owned concurrency and state

The library holds no token cache or lock. Callers must serialize refresh attempts
for the same credential generation across all users of that source, including
other processes. They should not start a second refresh with the same old token
while the first is unresolved. A successful new token must replace the old one
in the caller's authoritative generation; any late result must be checked against
the generation it used before updating a source. An unknown result must stop
blind retries and require a separately authorized recovery policy. This document
does not authorize persistence or implement an account credential manager.

Different independent synthetic tokens are tested concurrently only to establish
that no package-global token state crosses attempts. That test is not evidence
that concurrent use of one real refresh token is safe. Input strings and explicit
result getters contain secrets; all existing no-logging/no-redirect and cooperative
callback rules remain. No real credentials, service requests or long-lived access
were created or used.

Original tests compare strict/no-option/opt-in cases, absent/returned/same/null/
empty/duplicate fields, missing expiry, transport/Close/expired failures, invalid
option preflight and isolated concurrent attempts. Pure request/response helpers
remain covered by their existing tests, and the strict entry retains its negative
omission regression.
