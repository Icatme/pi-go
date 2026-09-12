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

## 2026-08-20 - pi-mono provider P0 parity

- Preserved ordered OpenAI-compatible `reasoning_details` in the existing
  thinking signature and replayed only validated summary, encrypted, and text
  details without adding a legacy tool-call compatibility path.
- Added Kimi top-level `cached_tokens` accounting with normalized usage totals,
  exposed DeepSeek V4 Flash low reasoning on OpenCode Go, and retried the exact
  transient request-buffer failure across pre-output Responses, Codex, and
  Completions SSE streams without replaying streams that already emitted output.
- Added streaming, replay, usage, request-wire, retry-classification, real SSE
  retry, and no-duplicate-output regressions. Verified root and examples tests
  and vet, `pkg/pigo` race tests, module integrity, and diff cleanliness; no live
  provider request was run.

## 2026-08-20 - OpenCode Go stability contracts

- Made OpenAI Responses honor explicit zero retries and classified structured
  usage-limit, quota, and billing 429 responses as terminal while retaining
  opt-in transient HTTP and pre-output SSE retries.
- Refreshed the stable OpenCode Go catalog to the 20 documented active models,
  corrected Anthropic routes, and added GPT-5.6 Luna max reasoning.
- Kept OpenCode Go usage token-only across Responses, Completions, and Anthropic
  aggregation even if price metadata is injected; aligned compatible cache
  aliases with pi-mono's documented precedence.
- Added catalog, routing, retry, cache precedence, and three-protocol token-only
  regressions. No paid live request, release, or deployment was performed.

## 2026-08-31 - v0.7.2 OpenCode Go structured output

- Fixed the model-scoped capability contract that rejected GPT-5.6 Luna
  `json_object` and strict `json_schema` output before sending a provider request.
- Kept mixed-protocol OpenCode Go capabilities scoped to Luna rather than
  advertising structured output for every OpenCode Go model.
- Added hermetic capability regressions and an opt-in live Luna structured-output
  test; verified the live OpenCode Go Responses endpoint with strict JSON Schema.

## 2026-08-31 - v0.7.3 stateless Responses reasoning replay

- Preserved `status` and `encrypted_content` from terminal Responses reasoning
  items so same-model stateless continuations can replay the complete provider
  payload instead of a lossy summary.
- Accepted terminal-only `reasoning_text` items when a provider omits reasoning
  summary events, covering native DeepSeek Responses as well as OpenAI.
- Added a hermetic terminal SSE regression and advanced the package release to
  v0.7.3 for ai-relay's portable Responses contract.

## 2026-08-31 - v0.8.0 provider/model capability snapshots

- Added an extensible capability query with exact model lookup and deterministic
  provider snapshots for wire API, streaming, tools, structured output,
  reasoning tiers, temperature, top_p, and parallel calls.
- Added exact OpenAI registry facts for `gpt-5.6`, `gpt-5.6-sol`,
  `gpt-5.6-terra`, and `gpt-5.6-luna`, including their Responses capability
  surface, reasoning levels, and limits.
- Unified Responses reasoning effort normalization through each model's
  `ThinkingLevelMap` and clamp semantics, keeping GPT-5.6 payloads aligned with
  the advertised `none`/`low`/`medium`/`high`/`xhigh`/`max` surface.
- Added typed `top_p` and optional `parallel_tool_calls` options for the OpenAI
  Responses adapter, preserving the existing default `true` while serializing
  explicit `false`; other wire APIs remain fail-closed for these capabilities.
- Added explicit `UsageReported` evidence across provider parsers, failed
  Responses, stream retry gates, hosted-tool usage/cost aggregation, Responses
  cache-write accounting, message cloning, and context replay so reported
  all-zero usage stays distinct from no usage.
- Kept omitted dynamic Command Code facts explicit and fail closed, without
  model-name inference or adding AI Relay routing and resilience policy.
- Added static, dynamic, mixed-wire, missing-fact, deep-copy, and concurrent
  catalog replacement regressions. Root and examples tests, race tests, vet,
  and module verification passed; tidy was content-clean apart from existing
  `go.sum` line endings. Bumped the feature version to `v0.8.0`; no live
  provider request, tag, or release was run.
- Replaced the OpenAI Responses API-wide sampling claim with exact model facts:
  GPT-5.1/5.2/5.4 allow `temperature` and `top_p` only at `none`, while the
  registered GPT-5.6 family records its `medium` default and supported levels;
  undocumented model/provider combinations remain unknown and fail closed.
- Reject invalid sampling/reasoning combinations before network dispatch, and
  expose each registered model's exact base endpoint in capability snapshots so
  downstream plans can freeze both capability and dispatch identity.
- Kept the capability contract unversioned and the package at `v0.8.0`; this
  follow-up adds fields directly without schema-version branches or aliases.
- Verified the follow-up with root and examples tests, race tests, vet, builds,
  and module verification; tidy remained content-clean apart from the existing
  `go.sum` line endings. No live provider request was run.
- Removed README wording that presented untagged `v0.8.0` as an existing
  release; installation now uses the version-neutral `@latest` form while the
  local package version remains `0.8.0`.

## 2026-09-03 - v0.8.1 failed Responses metadata

- Preserve the actual HTTP status, response headers, provider request ID, and
  `Retry-After` callback evidence when an OpenAI Responses request terminates
  on a non-2xx response before an SSE stream is established. This lets hosts
  distinguish a provider rate limit from a generic upstream failure and apply
  durable backoff without changing pi-go retry policy.
- Extended the existing no-retry usage-limit regression to prove one provider
  attempt and the complete 429 metadata callback. Bumped the patch release to
  `v0.8.1`; root tests, race tests, vet, and builds passed with `GOWORK=off`.
  The nested `switch-chat` example passed; `mattermost-chatbot` remains blocked
  by its pre-existing duplicate `google.golang.org/genproto` package graph,
  reproduced unchanged on the clean `v0.8.0` tag. No live provider request was
  added.

## 2026-09-07 - provider HTTP evidence and streaming boundaries

- Restrict `not_sent` evidence to the private standard transport created for a
  nil client. Preserve supplied clients and classify their failures as unknown,
  including registered protocols that hide an earlier successful dispatch.
- Preserve bounded error-body read failures and their error chains for SDK
  consumers. Keep redirect bodies lazy and bounded so unused long bodies do
  not stall normal redirects; close original bodies and observe each hop once.
- Add real local HTTP regressions for connection reuse, partial writes, proxies,
  TLS failure, cancellation, redirects, SDK entry points and cumulative SSE
  limits. Preserve successful streams, existing output and upstream call counts.
- Root-module tests, full race tests and vet passed with `GOWORK=off`; the SSE
  PR's exact original head also passed those checks independently. No real
  provider or production request was made.

## 2026-09-07 - v0.9.0 provider transport boundaries

- Advance the package and examples dependency to v0.9.0 for the HTTP delivery
  observation API, private default transport and bounded HTTP/SSE behavior.
- Keep supplied HTTP clients unchanged; callers that customized global HTTP
  clients or transports must pass their client explicitly to retain that setup.
- The source release includes the reviewed PR #10 and #11 commits; root and
  examples modules passed tests, full race tests and vet without skipped tests.

## 2026-09-07 - provider account quota queries

- Added the optional provider-level `QuotaQuerier` contract and Command Code
  account adapter, keeping balance pools, rolling windows, subscription data,
  and unavailable sections distinct from per-request model usage. Optional
  amounts follow the cc-switch usage-data reference; missing values stay unknown.
  Command Code keeps monthly/purchased/free pools separate with nonnegative
  remaining amounts in provider credits, without an assumed USD conversion.
  Window scope names only the monthly pool; purchased credits bypass those
  windows, following the official usage policy.
- Added `pigo quota [--json] <provider>` with existing CLI auth loading, separate
  balance/window output and known pool scope, partial-result warnings, and
  nonzero query/output errors.
  Documented provider registration, auth/context options, alpha endpoint scope,
  and the absence of model refresh, generation, retries, or quota caching.
- Verified focused quota CLI regressions, the full CLI package tests and race
  tests, and CLI vet with `GOWORK=off`; formatting and diff whitespace checks passed.
- Integration validation built the CLI and successfully read real Command Code
  credits, windows, and subscription using its three account GET endpoints.
  No model generation, account mutation, commit, push, or release was performed.
- Final validation passed the checked-in root/examples test script, full
  `pkg/pigo` and CLI race suites, root vet, and the checked-in CLI build script.
  The final compiled `bin/pigo.exe quota --json commandcode` returned the three
  credit pools, monthly-scoped windows, and subscription with no unavailable
  sections. All validation used `GOWORK=off`; live model tests stayed disabled.

## 2026-09-07 - v0.10.0 provider quota integration

- Rebase the quota feature onto the existing v0.9.0 mainline while preserving
  both the transport-boundary and account-quota work records.
- Advance the package version and nested examples dependency to v0.10.0 for
  the optional provider quota interface and Command Code account implementation.
- Root and examples modules passed tests, full race suites, and vet with
  `GOWORK=off` and live model tests disabled. Quota source was unchanged by
  integration; the version bump adds no further provider request.
- Integrate locally into master; remote publication and release tagging are
  outside this merge and version-bump step.

## 2026-09-11 - pi upstream and CommandCode v0.6.4 follow-up

- Follow the applicable AI/agent changes from [pi-mono f3c672245](https://github.com/badlogic/pi-mono/commit/f3c672245d25ef2283ffc0d9cdec8a5482651103)
  and [pi-commandcode-provider v0.6.4](https://github.com/patlux/pi-commandcode-provider/releases/tag/v0.6.4).
- Run next-turn preparation only after the stop decision and confirmation that
  another turn will run. Persist requested and effective thinking separately;
  model switches and runtime setters resolve them atomically, including restore
  and resume. Custom backends can supply a pure thinking resolver.
- Add explicit Responses cache-mode/output-limit controls, capability-aware
  Codex Off normalization, and linear accumulation of reasoning details without
  rewriting earlier event snapshots. Clone the activated compatibility switches.
- Update native/Codex catalogs with Astra and current DeepSeek Flash semantics,
  including user/tool images and top-level reasoning effort. Remove retired
  Codex GPT-5.4/mini and native deepseek-v4-flash entries; CLI/examples default to
  GPT-5.5. Add OpenCode Go session headers and current Mistral effort routing.
- Port CommandCode v0.6.4 metadata/pricing and native Provider API preference.
  Only a complete 403 upgrade_required response selects generate; route caching
  is bounded and isolated by endpoint and credential. Preserve HTTP evidence,
  one logical Observer lifecycle, and actual response protocol provenance for
  multi-turn reasoning/tool replay. Fix generate failure/abort handling,
  interrupted-tool history, image serialization, session headers and ZDR input.
- Final integration exposed a pre-existing Responses/Codex completion race:
  results could be returned before transport cleanup. A blocking-close regression
  failed on the original 38c3a4e HEAD in all four direct/simple SSE cases. Move
  successful stream finalization to the provider after body close/WebSocket
  release; the focused race regressions then passed ten consecutive runs.
- Final validation passed root and examples module `go test ./... -count=1`,
  `go test -race ./... -count=1`, and `go vet ./...`, all with GOWORK=off.
  Formatting/diff checks passed. Built `bin/pigo.exe` with the checked-in script
  and verified the compiled Codex/DeepSeek model listings. Live model tests
  stayed disabled; no dependencies, package version, commits, pushes or releases
  were changed. Removed the temporary baseline worktree after reproduction.
## 2026-09-11 - Follow-up review fixes

- Fix all four confirmed review findings without broadening the provider or
  agent surface. Add regressions that fail before each fix and pass afterward.
- Preserve dequeued steering/follow-up input when next-turn preparation fails
  or is canceled. Save it once in the durable transcript and invocation events;
  Run, Continue and tool-resume tests cover order, retry and temporary context.
- Keep inherited model defaults out of the snapshot's explicit model selection.
  Resolve the effective model and thinking per turn; test dynamic definitions,
  restore, clearing/setting an explicit model and temporary turn overrides.
- Preserve CommandCode Off support in its effort map. Catalog regressions and
  eight local HTTP agent cases cover default/explicit Off on Claude/OpenAI native
  requests and the upgrade-required generate fallback.
- Report uncached DeepSeek input separately from cache hits, and calculate the
  request cost from token usage instead of returning per-million-token rates.
  Local SSE tests cover Pro/Flash, direct/simple streams, Done/Result, omitted
  cache-miss counts, uncached/fully-cached input and reported zero usage.
- Independent review found no further issue in these fixes. Root and examples
  modules passed full tests, full race suites and vet with GOWORK=off; formatting
  and diff checks passed. Rebuilt bin/pigo.exe and checked its DeepSeek listing.
  Validation used local fixtures; live model tests remained disabled.

## 2026-09-11 - pi-go v0.11.0 module version

- Advance the package version and nested examples dependency to v0.11.0 for
  the upstream AI/agent follow-up, CommandCode v0.6.4 transport and catalog, and
  reviewed runtime/accounting fixes recorded above.
- Prepare the user-authorized master commit and annotated v0.11.0 module tag
  so JZBeacon and AI-Relay can pin a downloadable version.
- After the version bump, root and examples full tests, vet, tidy-diff and module
  verification passed with GOWORK=off. Normalize local checksum line endings
  for tidy-diff; checksum contents and external dependency versions are unchanged.
  Rebuilt the Windows CLI and verified its displayed version is 0.11.0.
  The preceding full race validation covers the unchanged implementation.

## 2026-09-11 - pi-go v0.11.1 completion event lifecycle fix

- Complete and CompleteSimple now drain their private event streams before
  returning, releasing dispatch goroutines and partial response snapshots.
- Preserve explicit Stream/Result event consumption and observer callbacks.
  Regression tests reproduce the old failure for success, error, and canceled
  completions with more than 1024 events; full root tests, focused race, and vet pass.
- Apply the same fix to the deployed 1ed9651 provider baseline as v0.9.1 for
  JZBeacon, whose immutable route snapshots are incompatible with newer catalogs.

## 2026-09-12 — Native Token Harbor provider

- Register Token Harbor as an independent built-in provider with its own API-key environment variable and Chat Completions endpoint. Move the existing Relay extension's three exact model definitions without changing their capability snapshots; Relay continues to own its Responses contract projections and reasoning conversion.
- Standalone tests exercise provider discovery, the exact catalog and API-key resolution through the Chat transport without importing ai-relay.
- Validation: full root tests, focused Token Harbor race tests, vet, build and formatting pass. All nine Relay model/contract snapshots match the original extension. After account email verification, host live checks succeeded for the native Token Harbor GPT-5.6 Luna route and the connection probe; the probe also completed a custom qwen3.8-flash model request. Single-attempt connection probes also returned HTTP 200 and complete nonempty text for deepseek-v4-flash (9715 ms) and glm-5.3-flash (1109 ms), using the bounded 2048-token / 30-second probe; the three named models were confirmed in the authenticated catalog. Publish only the codex/tokenharbor-native source branch; keep version constants and release tags unchanged.
## 2026-09-12 - Stream cancellation and bounded event buffering

- Complete the stream lifecycle repair with cancellable dispatch, an idempotent Close method, release of queued snapshots and context callbacks, and preservation of the provider's final result after cancellation.
- Bound pending events by total count and estimated payload, evict deltas first, and report a terminal error when lifecycle events exceed the budget. PrintStream now cancels its own run when its writer fails.
- Reproduce cancellation against mainline v0.11.1 and the PR using the same test over the actual package: the old implementation retains a dispatcher after provider completion; the repaired implementation releases it in all three repeated runs.
- Validation on f7b1c5b passed focused regressions repeated three times, full root tests, pkg/pigo race tests and go vet. This integration supports the authorized downstream AI-Relay and JZBeacon dependency updates; no production deployment is included.
## 2026-09-12 - Combine Token Harbor with the repaired stream mainline

- Merge master 39770712780917543dc53d5b358413d63e0ebd23 into the native-provider dependency branch, preserving stream cancellation, bounded pending events, the v0.11.1 completion fix, both worklog entries, and the exact Token Harbor catalog.
- Full root tests, vet, build, module verification and tidy-diff pass with GOWORK=off. The first concurrent full race run hit the existing delta-buffer test's two-second producer deadline; the unchanged test then passed three times both here and against downloaded 3977071. Full race with package concurrency limited to one passed without changing assertions or production behavior. Checksum line endings were normalized locally for tidy-diff; dependency content is unchanged.
- Keep source publication on codex/tokenharbor-native without a default-branch update, version constant change, tag or production action.
