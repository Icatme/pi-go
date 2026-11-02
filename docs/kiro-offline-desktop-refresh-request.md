# Offline desktop refresh request (community-observed)

The pinned community implementation defines
`https://prod.{region}.auth.desktop.kiro.dev/refreshToken` and sends POST JSON with
only refreshToken. Evidence:
[oauth.ts endpoint](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/oauth.ts#L32),
[desktop branch](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/oauth.ts#L372-L398).
This is NOT a verified official public API contract or permission to use it.
No live endpoint request has been made. Code/tests are independently authored.

The internal constructor requires caller scope, explicit user agent, refresh token,
context and encoded byte budget. Region has a bounded safe hostname shape and
profile must be nonempty, but neither endpoint availability nor account ownership
is inferred. The profile is not sent in the body/URL: it is retained as asserted
context for later response matching. There is no arbitrary endpoint override or
region/profile fallback.

Unlike upstream's fixed desktop identity, the helper has no default user agent.
Only the caller's explicit value is included. The body has exactly refreshToken;
no bearer header, IDC client secret, origin, invocation identifier or login grant
is invented. JSON preserves validated input punctuation and final escaped bytes
must fit the caller budget. Conservative token-size/control-character restrictions
are local subset choices, not service field limits.

No executor, desktop app/CLI launch, token discovery, client registration, browser
login, source mutation, persistence, refresh timer or retry is added. The returned
request body contains a secret and must not be logged/dumped/persisted. GetBody is
nil, but that alone cannot enforce a caller's redirect/retry behavior. Any later
sender must prohibit redirects and secret logging; real credential use or actual
calls require explicit authorization. Strings/buffers are not securely zeroized.

Three original tests cover exact synthetic URL/method/headers/single JSON field,
escaping and byte limits, diagnostic redaction, invalid region/profile/token/UA,
context cancellation and value-snapshot ownership. The later injected attempt and explicit public wrapper wire this request only to
a caller-supplied executor, never an automatic credential source.
