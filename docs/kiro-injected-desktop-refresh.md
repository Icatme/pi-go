# Injected desktop refresh attempt (synthetic only)

This internal path joins the community-observed desktop request to the already
strict desktop response parser. It calls only an explicit executor, once. It has
no default network, desktop/CLI launch, credential discovery, browser login,
source update, persistence, automatic retry or region/profile fallback.

The caller supplies asserted profile/region scope, explicit UA and refresh token,
request/response budgets, lifetime/skew and clock. Preflight and the first clock
are checked before executor invocation. The shared bounded JSON transport reader
requires HTTP200, accepted MIME/encoding, complete bounded body, known length and
successful Close. Only then is the ending clock observed and the response parsed.
Expiry is conservatively anchored to the first clock; backward or expired ending
observations reject. The same trustworthy clock must serve both observations.

A returned profileArn must exactly match the asserted profile. Null or a different
profile is rejected rather than silently adopting new account context. Absent
profile leaves the caller assertion, not proof of account identity. A genuinely
absent refreshToken retains the exact request token, matching the pinned desktop
branch; explicit null/empty still reject, and explicit rotation wins. The private
result provenance marks retention. This does not promise continuing token validity.

The returned boolean means only executor invocation. Any subsequent failure,
including read/Close/profile/clock/expiry/parser rejection, returns a zero result
and true: the server may already have rotated tokens. Do not retry or assume the
old token remains usable after an unknown outcome. Preflight/starting-clock failure
returns false. No raw transport, response or credential error is retained.

Executor/Read/Close/clock must cooperate with context. A callback can hide retries,
redirects or logging; this helper cannot force their absence and requires them to
be prohibited. Request and response buffers contain secrets and are not securely
zeroized. No source locks/cache are added; caller must serialize refreshes for one
credential generation and fence late results. Different provider flows remain
separate; IDC's strict/opt-in policy has not changed.

Evidence: [pinned desktop branch](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/oauth.ts#L372-L398),
[request contract](kiro-offline-desktop-refresh-request.md),
[expiry audit](kiro-refresh-audit.md). This community endpoint is not verified as
an official supported third-party API; real account/service compatibility is unknown.

Original synthetic tests cover exact request/snapshot, omission/rotation/matching
profile, Close delay without expiry extension, profile mismatch/null, token null/
empty, transport/redirect/header/length/budget/Close/cancel/time failures and no-call
preflight. No actual desktop account, real credential or service was used. This
internal attempt is not yet a public automatic authentication integration.
