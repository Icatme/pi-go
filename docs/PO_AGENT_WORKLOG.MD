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
- Added hermetic discovery, auth-shape, callback-server, browser-login, registry-swap, CLI-refresh, and gated live-request coverage; finalized the release version as `0.4.1`.
- Verified the real Provider API catalog (50 models, including the two models missing from the old snapshot), a live free-model generation, focused and repository-wide tests, vet, race tests, and `v0.4.1` Windows/Linux/macOS amd64/arm64 release builds.
- Prepared five `v0.4.1` platform archives plus `SHA256SUMS.txt` under `dist/release-v0.4.1`.

## 2026-07-29 - pi-agent-go dependency pin (migrated record)

- Updated the former `pi-agent-go` root and LangGraphGo modules to Go 1.26.2 and `github.com/Icatme/pi-go v0.3.0`, replacing development-only cross-repository paths with the tagged dependency.
- Updated the Codex session regression to omit `conversation_id` while preserving `session_id` and `x-client-request-id`.
- The original root and nested tests passed; the record also identified pre-existing root vet and race findings that were carried into the merge remediation below.

## 2026-08-04 - Merge pi-agent-go into pi-go

- Imported the full `pi-agent-go` Git history, moved the runtime to `agent`, renamed the package to `agent`, and changed imports directly to `github.com/Icatme/pi-go/agent` without a legacy compatibility package.
- Kept the dependency direction `agent -> pkg/pigo`; moved LangGraphGo and runnable examples into nested modules coordinated by `go.work`, isolating LangGraphGo and Mattermost dependencies from the root module.
- Preserved the uncommitted Codex header regression from the source worktree, removed captured example logs, and consolidated package documentation and testing entrypoints into this repository.
- Fixed imported test-only vet findings and synchronized parallel event collection so the merged repository passes race detection.
- Verified the focused Codex header and parallel-event regressions, `.\scripts\test.ps1`, `go vet ./...`, and `go test -race ./... -count=1` in the root, LangGraphGo, and examples modules.

## 2026-08-05 - Command Code v0.4.3 model-cache parity

- Aligned pigo with the latest stable `pi-commandcode-provider` v0.4.3 model-loading behavior: validated live discovery, atomic Pi-compatible v1 cache writes, cached offline fallback, and a non-fatal empty catalog on the first offline load.
- Added `COMMANDCODE_MODELS_CACHE` and `PI_CODING_AGENT_DIR` path handling, structured live/cache/empty results, registry replacement, and CLI warnings that preserve usable cached models.
- Verified compatibility by loading the cache written by the installed Pi plugin, simulating an offline provider, and completing a real `gpt-5.6-luna` request that returned `PIGO_ALIGNED`.
- Passed focused tests, `.\scripts\test.ps1` across the root, LangGraphGo, and examples modules, `go vet ./...`, and `go test -race ./... -count=1`.
