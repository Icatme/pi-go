# PO Agent Worklog

## 2026-07-28 - Core review remediation

- Removed stale provider-scope policy from `AGENTS.md`; provider scope remains a product/documentation concern rather than an agent rule.
- Returned refreshed OpenAI OAuth credentials through an external callback and coalesced concurrent refreshes without storing credentials in the library.
- Enforced request timeouts and cancellation, isolated cached Codex WebSockets by endpoint/account/session, rejected truncated Anthropic streams, and wired response/observer callbacks.
- Preserved message/context metadata during transforms and JSON round trips, including trailing synthetic tool results.
- Verified with focused regressions, `go test ./... -count=1`, `go vet ./...`, and `go test -race ./... -count=1`.

## 2026-07-28 - Upstream request header parity

- Aligned model and request header precedence with `pi-ai`, including Kimi's `KimiCLI/1.5` model header and request-level overrides used by agent extensions.
- Aligned OpenAI Codex SSE and WebSocket identity headers with upstream `pi`, including the platform UA and protected authentication, account, originator, and transport headers.
- Removed the non-upstream Codex `conversation_id` header and stopped generating an SSE request ID when no session ID is supplied.
- Verified with focused and race-enabled header regressions, `go test ./... -count=1`, `go vet ./...`, and Linux, Darwin, FreeBSD, Solaris, and wasm package builds.

## 2026-07-28 - Command Code provider

- Ported the actively maintained `pi-commandcode-provider` v0.4.2 wire protocol into the native `commandcode` provider and `commandcode-custom` API module.
- Matched the extension payload, streaming events, retry rules, model identity headers, and Node fetch user agent while preserving request-level header overrides.
- Kept credentials external through `COMMANDCODE_API_KEY`, explicit API keys, or caller-supplied auth; no Command Code browser login, auth-file reads, or persistence were added.
- Registered a deterministic snapshot of the public Provider API model list and the extension's known price table, avoiding hidden network I/O during provider initialization.
- Verified with focused protocol regressions, repeated Codex timeout coverage, `go test ./... -count=1`, `go vet ./...`, and `go test ./... -race -count=1`.

## 2026-07-29 - Command Code review remediation

- Replaced the incomplete extension-era price table with an explicit price entry for every built-in Command Code model, sourced from Command Code's current official pricing table; missing entries now fail closed instead of appearing free.
- Preserved standard JSON Schema tool parameters without lossy conversion and retained conversion only for `kind`-based internal schemas.
- Added Qwen 3.7 Plus long-context tier calculation and bumped the feature release version to `0.3.0`.
- Verified with focused Command Code regressions, `go test ./... -count=1`, `go vet ./...`, and `go test ./... -race -count=1`.

## 2026-08-02 - Command Code v0.4.2 capability parity

- Added validated Provider API model discovery and registry refresh, including `COMMANDCODE_MODELS_URL` and `COMMANDCODE_API_BASE`; the CLI refreshes before Command Code model listing and requests.
- Added browser-assisted `pigo login commandcode`, CSRF state validation, localhost callback/CORS handling, manual-key fallback, and v0.4.2-compatible environment and auth-file lookup.
- Retained the native Go streaming, retry, tool, usage, and pricing implementation; dynamically discovered models without known pricing follow upstream's zero display-cost behavior.
- Review remediation closes the localhost callback server before blocking on manual-key fallback and keeps the provider-agnostic model listing available with the registered catalog when live Command Code discovery is unavailable.
- Added hermetic discovery, auth-shape, callback-server, browser-login, registry-swap, CLI-refresh, and gated live-request coverage; bumped the feature version to `0.4.0`.
- Verified the real Provider API catalog (50 models, including the two models missing from the old snapshot), a live free-model generation, focused and repository-wide tests, vet, race tests, Windows release build, and Linux/Darwin/FreeBSD amd64 cross-builds.
- Built `bin/pigo.exe` (11,376,640 bytes, SHA-256 `c355c140ae85ccd568fab502d02864bd20a7b86ab72f4d87dd18c40075c72fca`).
