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

## 2026-08-09 - Agent runner and flat event streams

- Added a stateless asynchronous Runner with cloned snapshot boundaries,
  lossless bounded events, run metadata, and concurrent-safe Wait/Close.
- Flattened assistant update metadata onto AgentEvent and made AssistantStream
  cleanup explicit and cancellation-aware across the built-in adapters.
- Added lifecycle, backpressure, adapter, cancellation, race, and vet coverage;
  no live provider test was run for this change.

## 2026-08-09 - Durable agent sessions and compaction

- Added the `agent/session` child package with memory and versioned append-only
  JSONL repositories, lane branches, strict pure replay, and detached reads.
- Added provider-independent compaction that only cuts complete user turns and
  preserves assistant tool-call/result groups.
- Excluded pi-agent's explicitly pre-convergence operation/Harness protocol;
  session, race, vet, repository safety, and crash-tail tests passed without a
  live provider request.

## 2026-08-09 - Task agent tools and structured reflection

- Added a strict task-only `prebuilt.AgentTool` over isolated child Runners,
  including automatic parent-run lineage, transient child-event forwarding,
  and depth, per-child turn, and timeout limits.
- Kept child termination and internal events outside the parent transcript and
  converted only the final child output into a normal parent tool result.
- Replaced reflection keyword matching with typed accept/revise evaluations,
  strict JSON model output, complete request replay, and one evaluation for
  every generated draft.
- Added ordinary, race, vet, cancellation, ownership, lineage, and example
  coverage; no live provider request was run for this change.

## 2026-08-09 - Targeted tool approval checkpoints

- Added generic whole-batch tool gating and exact pending-batch resume to the
  single-agent core without adding graph or supervisor orchestration.
- Added `agent/checkpoint` with strict versioned envelopes, revisioned CAS,
  targeted partial approval/rejection, single-use capabilities, and a
  process-local memory store.
- Kept resumable bindings static by rejecting `ToolResolver` and
  `PrepareNextTurn`; canonical argument bindings re-interrupt on parser/hook
  drift, survive JSON number/type normalization, and preserve the original
  MaxTurns budget across attempts.
- Made pending state fail closed across Run/Continue/Resume, and made
  pre-execution resume failures return the intact original batch to a new set of
  single-use interrupt IDs before exposing the terminal event.
- Documented the trusted secret-bearing Store boundary and the non-exactly-once
  terminal-CAS failure case (`StatusIndeterminate`, `Persisted=false`).
- Added ordinary, race, vet, strict-codec, concurrency, lifecycle, ownership,
  stale-decision, and side-effect regressions; no live provider request was run.

## 2026-08-11 - Bounded external input turn loop

- Added the `agent/turnloop` child package as a bounded, non-blocking external
  input boundary over sequential stateless Runner invocations and one persistent
  snapshot.
- Added explicit next-run, steering, and follow-up delivery, graceful draining,
  cooperative immediate cancellation, Runner-compatible cloned results, and
  single-consumer ordered event draining without moving queue policy into the
  root runtime.
- Kept Eino-style active-turn safe-point preemption, graph orchestration,
  checkpoint resume, and persistence out of this layer; no live provider
  request was run for this change.

## 2026-08-11 - v0.7.0 agent runtime release

- Merged the six reviewed agent-runtime PRs in dependency order while
  preserving their commit ancestry on `master`.
- Finalized the pre-1.0 minor release as `v0.7.0` for the new Runner, session,
  AgentTool, checkpoint/HITL, TurnLoop, and core-safety contracts.
- Updated the package version, examples dependency pin, and release-facing
  agent installation guidance without modifying unrelated dependency versions.
- Verified root and examples tests, race tests, vet, module integrity, semantic
  tidy state, and diff cleanliness; no live provider request was run.

## 2026-08-12 - v0.7.1 CommandCode usage accounting release

- Fixed CommandCode cached input accounting so `Usage.Input` contains only
  uncached input, preferring `noCacheTokens` and otherwise subtracting cache
  reads and writes from the provider-reported total input.
- Counted cache writes in CommandCode long-context tier selection and added
  regression coverage for explicit, derived, malformed, and clamped usage.
- Bumped the patch release surfaces to `v0.7.1`; no `ai-relay` compatibility
  workaround was added because the defect belonged to the provider parser.
- Verified root and examples tests, race tests, vet, module integrity, semantic
  tidy state, and diff cleanliness; no live CommandCode request was run.
