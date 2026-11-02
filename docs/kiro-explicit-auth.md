# Explicit offline authentication headers

This independently written helper follows the community wire fields in
pi-provider-kiro dd93f2e `src/oauth.ts` L66–77 and `src/token-type.ts` L43–44:
Bearer Authorization, plus API_KEY or EXTERNAL_IDP tokentype where explicitly
selected. Unlike the community plugin, it does not inspect unverified JWT
claims to infer the external IdP kind: the caller must choose the kind.

`buildKiroExplicitAuthHeaders` receives a token and declared kind directly.
No environment/CLI/session/keychain reads, credential manager, login, token
refresh, global cache, persistence, host calls, logging or network are added.
The function is not wired into #68's HTTP constructor or any registered provider.
Tests use visibly invented LOCAL_ONLY strings only; no real token is required.

The input subset is bounded to 16 KiB and the RFC 6750 section 2.1 b64token
character grammar with trailing padding only; whitespace/control/non-ASCII,
commas and padding in the middle are rejected. See
https://www.rfc-editor.org/rfc/rfc6750#section-2.1 .
An API key must have a nonempty ksk_ suffix; an explicit ordinary/external bearer
kind cannot silently treat a ksk_ value as a different credential kind. These
are local syntax/configuration checks, NOT signature validation, expiration,
identity, token validity, scope or permission checks. Opaque external tokens are
accepted only because the caller explicitly asserts that kind; no JWT trust is
inferred. Unsupported token encodings are rejected rather than normalized.

## Secret boundary

Successful output maps contain the full Authorization secret. They are intended
only for an explicitly scoped future request operation: never fmt/log/JSON-dump
these maps or attach them to errors. All errors here are one fixed message and
contain no input token or claims. Separate calls return separate maps; strings
may remain in Go-managed memory and **no zeroization or general logging safety
is claimed**. The future HTTP layer still needs account/region/endpoint binding,
redirect policy, secret-safe tracing and authorized credential lifetime control.
The helper itself must not be mistaken for a credential store or permission gate.

## Verification status

Test sources cover all three explicit kinds, exact fields, character/padding
rejection, ksk_ mismatch, generic errors, 16384/16385-byte limits, independent
maps and no JWT-like automatic kind change. No test prints supplied tokens.
Go compilation, gofmt, unit/race tests have NOT run (approved compatible compiler
unavailable); no installation or CI dispatch. No actual account authentication,
service call or spending happened. This draft is independent of #62/#65/#68,
based on #61 and not merged into master or deployed.
