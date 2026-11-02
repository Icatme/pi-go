# Kiro application-layer client identity fixtures

This draft is an independently written, pure offline part of client simulation.
It follows the application-header strings observed in community plugin
`mikeyobrien/pi-provider-kiro` commit `dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba`,
`src/stream.ts` lines 1015–1048. This is not a Kiro-official SDK contract.

## Explicit paths

- `buildKiroOfflineClientHeaders(userAgent, invocationID)` uses precisely the
  caller's declared UA. It does not silently identify pi-go as Kiro or AmazonQ.
- `kiroCommunityCLIUserAgent(instanceID)` is the separately and explicitly called
  compatibility template: the upstream aws-sdk-rust / AmazonQ-For-CLI string.
  The supplied 32 lower-case hex characters replace the upstream per-attempt
  random UUID without hyphens. They are NOT read from the user's machine.
- Both user-agent headers receive the same caller-supplied value. Other fixed
  fields are application/json, application/vnd.amazon.eventstream,
  codewhisperer optout=true, sdk invocation ID, attempt=1; max=1 and mode=vibe.
- UUID injection checks canonical lower-case hex/hyphen shape only, not version,
  random quality, identity ownership or uniqueness. The future transport must
  own fresh per-attempt values and a real retry policy. Zero IDs are permitted
  for deterministic fixtures, not endorsed as production invocation identity.

No Authorization or tokentype is supplied. There is no token discovery, login,
refresh, profile selection, HTTP connection, endpoint selection, provider
registration or default identity mode. Header maps are independently owned.
ASCII UA bounds and control-character rejection prevent CR/LF header injection
in this builder; arbitrary additional caller headers are not validated here.
The source contains no randomness and never examines host/OS/environment state.

## What this is NOT

This is application-layer string construction, not a real AWS SDK/CLI execution,
TLS/HTTP2/browser fingerprint emulation or a proof of accepted service identity.
The opt-out header is reproduced, not a claim about server-side privacy behavior.
Origin in the JSON body remains a separate explicit request-envelope input.
No public integration selects or combines these helpers automatically.
Service-use authorization and live-call scope remain separate gates: a simulated
UA or a working official CLI/API key does not prove third-party access is allowed.

## Verification

Go test sources assert the exact pinned compatibility template and entire map,
explicit declared identity, no auth headers, independent mutation, malformed /
CRLF / Unicode / overlong UA rejection, UUID shape, and exact 512-byte boundary.
Go compilation, gofmt, unit and race tests have NOT been run (no usable approved
Go toolchain in the current environment). No CI dispatch or real call occurred.

The draft is based on #61 and independent of #62's body encoder and #63's
TextContent projection change. After an approved toolchain is available, run
`go test ./pkg/pigo -run TestKiroClientHeaders -count=1`, then all Kiro tests,
race and full suite. Integrating these pieces into a registered HTTP provider
requires its own fake-transport cancellation/errors/redirect/auth tests before
any separately authorized real-service check.
