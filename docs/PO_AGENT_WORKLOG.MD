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

## 2026-08-05 - Remove unused LangGraphGo adapter

- Deleted the `adapters/langgraphgo` nested module, including its node, session, supervisor, and adapter-only tests.
- Removed the adapter from the repository test script and current architecture/runtime documentation; retained graph and multi-agent orchestration as explicitly outside the `agent` core boundary.
- Removed the now-unnecessary `go.work` and `go.work.sum`; the examples module already resolves the root through its local `replace`, and independent module loading avoids its legacy Mattermost graph conflicting with split genproto modules.
- Made the multi-module test script set and restore `GOWORK=off` so each module is validated against its own dependency graph.
- Verified the root and examples modules with `.\scripts\test.ps1`, `go vet ./...`, and `go test -race ./... -count=1` under `GOWORK=off`.

## 2026-08-05 - Public repository metadata and README

- Added a concise GitHub About description, a pkg.go.dev homepage, and provider/runtime topics for the public repository.
- Updated the root README with public-facing highlights, release and package-documentation badges, install commands, package navigation, and links to agent documentation and examples.
- Clarified that the merged `agent` package is available from `master` but is not part of the current `v0.4.1` tag.
- Verified all relative README links, `git diff --check`, root and examples tests through `.\scripts\test.ps1`, and `go vet ./...` in both modules with `GOWORK=off`.

## 2026-08-08 - OpenCode Go provider

- Added the native `opencode-go` provider with `OPENCODE_API_KEY` authentication and a deterministic 2026-08-08 snapshot of Pi's 18 active, tool-capable models from models.dev.
- Matched Pi's per-model routing across OpenAI Chat Completions, OpenAI Responses, and Anthropic Messages, including the MiniMax M2.7 and Qwen 3.6 overrides.
- Added the shared `openai-completions` transport needed by the provider, with reasoning replay, Kimi/DeepSeek/Qwen thinking controls, tool calls, images, usage, cache fields, retries, and terminal-event validation.
- Closed review findings by keeping parallel tool results contiguous before image attachments, preserving `cache_write_tokens`, and applying cloned model-level long-context pricing tiers from models.dev.
- Added hermetic protocol/routing coverage and a gated live OpenCode Go request; no live subscription request was run during this change.
- Finalized the backward-compatible feature release as `v0.6.0`, including the package version, examples dependency pin, and release-facing agent installation guidance.
- Verified with focused provider and CLI tests, `go test ./... -count=1`, `.\scripts\test.ps1`, `go vet ./...` in the root and examples modules, and `git diff --check` under `GOWORK=off`.

## 2026-08-09 - Agent core safety parity

- Hardened the single-agent loop against truncated tool calls, active reset,
  late progress, cancellation during preflight, and hook-mutated invalid args.
- Added per-tool sequential execution, all-results termination hints,
  turn-boundary prepare/stop hooks, automatic transport defaults, cached
  WebSocket selection, and model-aware maximum reasoning support.
- Added pinned, actively maintained JSON Schema validation with focused offline
  and race regressions; no live provider test was run for this change.
