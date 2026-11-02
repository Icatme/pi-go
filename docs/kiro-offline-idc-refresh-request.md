# Offline IDC refresh request construction

This internal draft constructs a request only. It has no executor, login, client
registration, credential discovery, CLI/IDE import, timer, persistence, retry,
redirect, source update or service call. Tests use only visibly synthetic secrets.
No public refresh API is added yet.

Evidence checked on 2026-11-02:
- [AWS IAM Identity Center CreateToken](https://docs.aws.amazon.com/singlesignon/latest/OIDCAPIReference/API_CreateToken.html)
  documents JSON POST `/token`, clientId/clientSecret, refreshToken and the
  `refresh_token` grant. This does not establish entitlement for a given client.
- [Community oauth.ts at dd93f2e](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/oauth.ts#L442-L467)
  supplies the regional `oidc.<region>.amazonaws.com/token` IDC construction.
  The Go scenarios and implementation are independently authored, not copied.

`newKiroOfflineIDCRefreshRequest` requires context, standard-partition region,
explicit user agent, client ID, client secret, refresh token and encoded byte
budget. It sends no scope/profile identifiers, bearer header or fabricated CLI
identity. JSON encoding preserves the exact validated values, including reserved
punctuation; the final escaped body must fit the caller budget (at most 1 MiB).
Each credential input has a defensive 16 KiB limit; unsupported control/invalid
UTF-8/empty values fail with a fixed error. These are conservative local limits,
not a claim about AWS service field maxima.

The host is fixed to the AWS suffix with no endpoint override. China, GovCloud
and isolated partition region names are rejected rather than guessing suffixes.
A syntactically accepted region is not proof the endpoint exists, that the client
was registered there, or that refresh is authorized. Region fallback is absent.
External IdP and desktop endpoints need separate contracts and are not supported
by this function. API keys do not use this refresh-token flow.

The returned `http.Request` body **contains secrets**. Do not format/dump/log the
request, read it into telemetry or persist it. Input-option JSON/fmt redact the
secret values, but they do not sanitize an explicitly obtained request/body.
`GetBody` is intentionally nil; this removes the standard convenience replay
hook, not a guarantee against a caller buffering and retrying. No transport is
installed and no redirect/retry authority is granted. Go strings and JSON byte
buffers are not securely zeroized by garbage collection.

Original tests cover exact URL/method/headers/four JSON fields, escaped punctuation,
byte equality and exact budget, secret-free options/URL/headers, input copies,
invalid scope/partitions/UTF-8/secret/UA/limits, nil/cancelled context and retained
request cancellation. The earlier refresh response parser remains a distinct
strict component; this request does not consume a response or update credentials.
Any future sender must prohibit redirects, especially cross-host forwarding of
this secret body, and needs explicit authorization before real credential use or
service calls. An HTTP client can otherwise redirect a POST regardless of this
constructor's intent; construction alone does not enforce transport policy.
