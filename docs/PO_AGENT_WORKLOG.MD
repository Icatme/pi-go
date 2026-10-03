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
## 2026-09-12 - Stream cancellation and bounded event buffering

- Complete the stream lifecycle repair with cancellable dispatch, an idempotent Close method, release of queued snapshots and context callbacks, and preservation of the provider's final result after cancellation.
- Bound pending events by total count and estimated payload, evict deltas first, and report a terminal error when lifecycle events exceed the budget. PrintStream now cancels its own run when its writer fails.
- Reproduce cancellation against mainline v0.11.1 and the PR using the same test over the actual package: the old implementation retains a dispatcher after provider completion; the repaired implementation releases it in all three repeated runs.
- Validation on f7b1c5b passed focused regressions repeated three times, full root tests, pkg/pigo race tests and go vet. This integration supports the authorized downstream AI-Relay and JZBeacon dependency updates; no production deployment is included.

## 2026-09-12 - pi-go v0.11.2 GitHub release

- Advance the package version and nested examples dependency to v0.11.2 for the merged stream cancellation, bounded event buffering, and writer-failure cancellation repairs.
- Validate root and examples with GOWORK=off: full tests, full race suites, vet, module verification, and tidy-diff all pass. Normalize checkout-only checksum line endings; checksum contents and third-party dependency versions are unchanged. Live provider tests remain disabled.
- Prepare the authorized annotated tag and GitHub Release from this mainline candidate. Build the Windows amd64, Linux amd64/arm64, and macOS amd64/arm64 CLI archives from the resulting clean commit, with SHA256 checksums and remote download verification.

## 2026-09-22 - pi-mono provider correctness follow-up

- Follow the applicable provider fixes through [pi-mono v0.87.0](https://github.com/earendil-works/pi/releases/tag/v0.87.0): Google model-specific thinking mappings, Cerebras-only bodyless overflow detection, z.ai prompt-too-long recognition, Cloudflare HTTP 520 and Azure peak-load retry classification.
- Resolve Google thinking from model capabilities before mapping either discrete levels or token budgets. Omitted/Off reasoning disables thinking when supported, otherwise selects the lowest supported level without requesting thought summaries. Invalid mappings fail before HTTP I/O. Preserve custom thinking budgets through provider option conversions.
- Wire-level regression tests exposed that Google requests used the SDK's `config` envelope directly against REST, and `omitempty` dropped the zero budget needed to disable thinking. Emit `generationConfig` with explicit zero budgets, and put system instructions, tools and tool choice at the REST root. The model remains in the URL; Google `OnPayload` now receives this corrected REST shape instead of the former `model/config` envelope.
- Mark built-in Gemini 2.5 Pro Off as unsupported and use its minimum 128-token budget, consistent with [Google's Generate Content thinking contract](https://ai.google.dev/gemini-api/docs/generate-content/thinking). Flash still sends zero when disabled. No model-name fallback overrides declared effort mappings.
- Preserve retry bounds, quota/billing exclusions, one start event, partial output, and the no-retry-after-output boundary. Local HTTP/SSE tests cover both Responses and Completions, retry exhaustion, explicit zero retries, and capacity errors before/after visible output.
- Regression tests reproduced the old failures before repair. Focused tests and root/examples full tests passed; final root and examples `go test -race ./... -count=1` and `go vet ./...` passed with `GOWORK=off` and live provider tests disabled. Formatting and diff checks passed. No dependency/version changes, commits, pushes, or releases are included. Transcript and agent lifecycle interface migrations remain separate work.
- Fix the three follow-up review findings: resolve Google custom budgets per mapped level so partial overrides preserve the other levels' defaults, and use dynamic budget `-1` for unrecognized token-budget models. Document zero-valued custom budget fields as unset; explicit Off still sends zero when supported, while Pro retains its minimum budget.
- Recognize Cerebras bodyless HTTP 400/413 errors in the Go Completions transport's actual error format as well as the SDK format. Keep the rule provider-scoped and exclude other statuses and nonempty unrelated errors without changing transport error messages.
- Add HTTP request/response regressions for partial budgets across all levels, mapped defaults, Pro omitted/Off minimums, dynamic/custom budgets on unknown models, and Cerebras positive/negative cases across providers. The new tests failed before the fixes and passed afterward. Follow-up validation passed focused Google/overflow tests, root `go test -race ./... -count=1`, root `go vet ./...`, examples `go test ./... -count=1`, formatting and diff checks with `GOWORK=off` and `PIGO_LIVE_TEST=0`; no live-provider calls were made.
- Bump the package version and nested examples dependency to `v0.11.3`, including the provider correctness changes and all three review fixes in the local commit. Root and examples `go test ./... -count=1` passed again after the version bump with `GOWORK=off` and `PIGO_LIVE_TEST=0`; formatting and diff checks passed. This step does not create or push a tag or publish a GitHub Release.


## 2026-10-01 - pi-mono AI and agent follow-up through v0.99.2

- Follow the applicable changes from v0.87.0 through pi-mono origin/main
  `8ce69e9d2b171d173fe4b6b2b6256f1f4411e69d` (v0.99.2 plus 17 commits),
  read on 2026-10-01. Scope is provider correctness, catalog metadata and
  single-agent lifecycle/transcript semantics. Codemode/MCP, classifiers,
  image generation, new ChatGPT OAuth and experimental pi-durable are separate
  work; dynamic provider tool anchoring is not implemented.
- Track Responses function calls by item ID, call ID and output index rather
  than one current slot. Reject unfinished, ambiguous or malformed tool-use
  completion; preserve terminal reconstruction, partial error output and the
  no-retry-after-output boundary. SSE, Codex SSE and WebSocket regressions
  reproduce the old incomplete/interleaved call failures before repair.
- Map Mistral reasoning through declared ThinkingLevelMap for direct/simple,
  explicit Off and omitted preferences. Correct priority cost multipliers
  (GPT-5.5 2.5x, other models 2x), preserve same-model empty Anthropic signatures
  only with explicit capability, and recognize Z.AI CN prompt-length overflow.
  Claude 5.5 adaptive reasoning and unsupported Off/temperature follow metadata.
  New provider regressions failed before the corresponding repairs and passed
  afterward; no retries or fallback paths were added to mask protocol failures.
- Add current GPT-6 Sol/Luna/6.1 Sol, Claude Opus/Sonnet 5.5, Mistral and OpenCode
  stable model metadata from the upstream generator, models.dev and official
  provider docs. Remove confirmed deprecated OpenCode models; retain GLM-5.1
  whose absence did not establish retirement. Correct MiniMax/Qwen compatibility,
  native GPT-5.6 supported IDs/default context caps and Codex Sol costs. Set
  native mid-conversation-system support only from the upstream exact allowlist.
  Preserve the OpenCode token-only/zero-cost contract. The CLI default now uses
  active Kimi K3 rather than retired Kimi K2.6.
- Add OnProviderStreamEvent copied RawMessage/model callbacks across all current
  SSE/WS/generate protocols and option conversions, including the default agent
  adapter. Preserve unknown fields and exact JSON numbers before normalization;
  propagate callback causes and prevent retry or Codex transport fallback after
  callback failure. Existing Observer implementations need no new method.
- Replace ShouldStopAfterTurn with FinishTurn and AgentTurnDecision. Run it
  before TurnEnd for finalized assistant/tool turns, including error/aborted
  output and canceled resumed batches whose tool results committed. End preserves
  unread queues; Continue guarantees one more model request. PrepareRequest runs
  after admitted input/tool resolution and validates final replacement tools
  before I/O without rerunning the resolver. Add non-consuming PeekQueuedMessages
  and persist effective assistant ThinkingLevel through snapshots/provider JSON.
- Add declaration-only SystemMessage history: appended content, named section
  replacement/removal, tool additions/removals, clone/JSON round trips, provider
  normalization, native OpenAI system-position replay and collapse for other
  protocols. Initial configuration seeds a leading system message; restore and
  session compaction retain the resolved prompt/sections/current tools. Go sorts
  section keys and uses string system content. Request-local context pruning
  preserves durable history without duplicating declarations. Remove
  SetSystemPrompt; use appended system messages (Steer while a run is active).
- Add ToolResult.StructuredContent and explicit IsError, declaration OutputSchema,
  and public RunToolCall sharing argument validation and before/after hooks.
  Content replacement clears stale structured JSON; invalid structured JSON or
  non-serializable output schema is rejected. OutputSchema is metadata, not
  automatic output validation. Checkpoint runners reject PrepareRequest as they
  reject non-durable PrepareNextTurn overrides. Update in-repo callers, examples,
  tests and runtime migration docs without keeping old hook/setter aliases.
- Independent review exposed two new-hook boundary holes: replacement tools
  skipped schema validation, and canceled committed resume skipped FinishTurn.
  Both are corrected with regression tests. A standalone RunToolCall initially
  used gated preparation without a gate; reuse the ordinary preparation path.
- Validation with GOWORK=off and PIGO_LIVE_TEST=0: root and examples full tests,
  full race suites and vet pass. Final output-schema/error-reporting adjustments
  also pass all agent race tests. Formatting and diff checks pass. All protocol
  tests use local fixtures; live provider/account availability and UI acceptance
  are not claimed. No new third-party dependency, version bump, commit, push,
  tag or release is included; preserve the existing unpushed 0.11.3 commit.

## 2026-10-01: Repair the seven upstream-follow-up review findings

- Replay existing system history at any position without injecting its rendered
  prompt again. Reset retains one resolved system baseline with named sections
  and current tool declarations; both Agent and Engine reject system-only
  continuation without calling the model or consuming queued input.
- Fold an explicit prepared request prompt before transformation, then derive
  the model request from the transformed/converted messages. The provider
  adapter preserves that history, including removal of all system messages.
  Detached transformation input keeps durable history unchanged. An explicitly
  empty transcript remains empty in preparation rather than restoring config.
- Follow-up input now satisfies FinishTurn Continue before considering a request
  with no new input. Correct the earlier three-request test to require two,
  including a MaxTurns=2 regression, matching the pinned upstream scheduling.
- Public pigo transcript helpers accept value and pointer system messages.
  Serialization normalizes valid message pointers and rejects nil pointers with
  an error rather than panicking; section/tool metadata survives round trips.
- switch-chat stores its replaceable preset prompt in a named system section.
  Existing leading preset content is migrated when needed, preserving dialogue,
  later system changes and other declarations. Updated presets take effect in
  existing sessions without adding duplicate patches on reload.
- Regression cases reproduced the review failures before repair. Root and
  examples full tests, full race suites and vet pass with GOWORK=off and
  PIGO_LIVE_TEST=0. Formatting and diff checks pass. Provider tests use local HTTP
  fixtures; live provider availability and UI acceptance remain outside this
  validation. No dependency/version changes, commit, push, tag or release.

## 2026-10-01: Bump pi-go to v0.12.0 and prepare the master push

- Bump pkg/pigo.Version and the examples module requirement from 0.11.3 to
  0.12.0. Use a minor increment for new runtime/protocol capabilities and the
  removal of ShouldStopAfterTurn/SetSystemPrompt in favor of the new APIs.
- Include the validated upstream follow-up and seven review repairs in one
  source commit. Preserve the existing 53e37ca v0.11.3 commit as an ancestor;
  origin/master is unchanged after fetch and is one commit behind the old HEAD.
- Root/examples full tests, race and vet passed in the repair pass. After the
  version-only change, CLI tests and all examples tests pass; examples resolves
  the local module as v0.12.0. A compiled native CLI prints pigo 0.12.0.

## 2026-10-02: Design MCP Codemode with wazero

- Add docs/design-codemode-wazero.md as a draft based on Pi v1.0.0 and the
  official MCP Go SDK v1.8.0 / protocol 2026-07-28. Separate WASM execution,
  QuickJS scripting, MCP connectivity, exposure/search, nested tool lifecycle,
  branch storage and the task-facing application boundary.
- Verify dependency maintenance and Go-version requirements. Correct the Pi
  1.0 exposure baseline to four modes; MCP codemode maps to generic deferred.
  Specify hidden enforcement at discovery and execution, ordered pattern rules,
  cancellation, stdio process-tree ownership and checkpoint limitations.
- Re-run temporary compiled Windows probes outside the repository: QuickJS
  async calls, heap and loop deadlines, SDK default modern protocol, and HTTP
  JSON raw-number preservation under modern and legacy protocol versions.
  Record prototype limits, including unfinished SSE/stdio/concurrent raw capture
  and production trap mapping. These are feasibility evidence, not full parity.
- Documentation only. No product source/dependency/version changes, root or
  examples test runs, live provider checks, UI acceptance, commit or publication.
- Consolidate the two design reviews against master 5907151 and the exact
  unmerged PR14 head c10ba730dc2ca8063631433f2fda11dcaf52e045. Keep the selected
  route; narrow the first delivery to its caller-owned MCP sessions and explicit
  whitelist, with no additional task Runtime. PR14 tests were not rerun here.
- Specify machine-readable errors/execution facts, nested Suspend rejection,
  two-level scheduling, after-hook result projection, complete basic ledgers,
  pre-clone event budgets and host permits retained until actual executor exit.
  Separate raw/SDK cache/identity binding and atomic branch CAS into gated phases;
  document independent OAuth/HTTP POST replay control and final permission checks.
- Add a repository-external compiled Windows stack probe using the same pinned
  WASM. At 64 KiB and 512 KiB it catches RangeError and continues with 1+2=3.
  Correct the probe's initial InternalError expectation to the observed exception.
  Update acceptance cases and evidence limits; only documentation changes in repo.

## 2026-10-02: Review and merge PR13 and PR14

- Review PR13 at 533105a and stacked PR14 at c10ba73 in an isolated managed
  worktree. The optional MCP adapter does not implement the proposed Codemode
  runtime; the later execution-contract gates remain required.
- Reproduce examples tests failing before compilation with -mod=readonly:
  the root MCP SDK graph selects x/sys v0.41.0 but examples still requires
  v0.37.0. Synchronize only examples/go.mod and examples/go.sum in commit
  49910c79cf6266d0a29e31a26dfde39bb1536a64; readonly examples validation then passes.
- Confirm official SDK v1.8.0 was released on 2026-09-14 and upstream had a
  2026-10-01 commit. Its Go 1.25 minimum is compatible with the local Go 1.26.4.
- With GOWORK=off and both live-test flags disabled, focused Token Harbor/MCP
  tests, all root and examples tests, root/examples vet and build, and both
  module verifications pass. Race suites pass for pkg/pigo, agent and
  agent/mcptools. Formatting/diff checks pass; no live provider or UI acceptance.
- Mark both drafts ready and merge PR13 with its exact head, retaining ancestry
  at 75f0258106c2e4bb26b848de4c3aab411943201a. Retarget PR14 to master, push the
  integration fix, and merge its exact validated head at
  4398f8565ab82c460ddafc5fcfc20a35ef1187db. Linux CI passes for PR13's merge
  (run 36957879920) and PR14's final head (run 36957944107).
- Verify both PRs are merged, both reviewed heads are ancestors of master, and
  the final master tree equals the validated 49910c7 tree. Fast-forward the local
  master to 4398f85 after verifying the pending worklog and design bytes remain
  unchanged. Update the design's implementation baseline without committing the
  pending design work; no version bump, tag or release.

## 2026-10-02: Implement the first MCP Codemode delivery

- Commit and push the accepted design/worklog to master at e4de3c8, after
  verifying PR13 and PR14 are merged. Implement M1/M2/M3a in an isolated managed
  worktree on codex/mcp-codemode-minimal, with explicit sandbox/core/binding
  ownership and parent integration. No version bump, tag or release.
- Add standalone codemode with wazero v1.12.0 and embedded quickjs-wasi 3.6.2
  (637405 bytes, SHA256 d4c9375f2b1ca4dc95f72c8aa2982a7a9951ac8011490d79c6582df732b4bbd9).
  Retain the exact source ABI, MIT notices and digest-preserving attributes;
  verify current maintenance, Go1.25 compatibility and native Windows execution.
- Bound heap/linear memory/native stack, code/catalog/arguments/results,
  admission/completion bytes, output and diagnostic retention. Provide Promise
  orchestration, BM25 discovery, source options and invocation-local Store with
  atomic revision CAS/success-only commits. No hidden default timeout; the local
  service example explicitly sets five minutes. UTF-16 value caps and physical
  UTF-8 store caps are checked separately.
- Replace ToolExecutorFunc positional arguments with ToolExecutionContext in
  core, prebuilt, existing MCP tools and examples. Unify model/direct/child
  lifecycle; retain typed failures, original Go chains, partial results and
  independent local/remote execution facts. Check current permission after
  scheduling and in mcptools immediately before CallTool.
- Add explicit two-level ChildCaller capabilities, FIFO for sequential policy,
  actual-exit permits, complete 1024-call basic records and bounded after-hook
  child events. Reject nested Suspend without pending state or script replay;
  static checkpoints reject containers. Go-only host-owned ChildCalls survives
  parent hook failure independently of discarded raw Content/Details.
- Add agent/codemodetool and examples/mcp-codemode using caller-owned official
  SDK sessions, fixed explicit native/MCP leaf allowlists and only one model
  code declaration. Validate effective post-hook projections before events and
  records. Unsafe JS numbers reject before dispatch or after response with
  appropriate facts/reason codes; tool-declared MCP failures resolve envelopes.
- With GOWORK=off and both live-test flags disabled, native Windows Go1.26.4
  root/examples ordinary tests, full race suites, vet, build and module verify
  pass. Formatting for all 47 changed/new Go files passes with normalized line
  endings. The compiled local example returns 50 open issues and titles 3/6/9
  from 150 long-body records in four host calls.
- Regression coverage includes catchable 64/512-KiB stack limits, heap/linear
  limits, CPU/microtask deadlines, unsafe JSON, failed Store commits/CAS conflicts,
  schema drift, queued permission revocation, nested Suspend after a write,
  hook redaction/original error chains, all basic records beyond detail limits,
  actual host exit after VM cancellation, and bounded Close outstanding lists.
- Add CI execution/race and compiled-example matrix for Windows amd64, Linux
  amd64/arm64 and macOS amd64/arm64; execution results are reported by PR checks.
  Keep raw/cache/identity binding (M3b), managed connections/dynamic hidden
  exposure/transport replay guards (M4), and durable branch CAS (M5) separate.
  No raw-wire precision, arbitrary-session at-most-once, live provider or UI claim.

## 2026-10-02: Fix PR15 execution and JSON boundary findings

- Repair the eight review findings on codex/mcp-codemode-minimal: strict Unicode,
  executor panic cleanup, private error correlation, preserved remote facts,
  final argument budgets, failure partial-result budgets, child summary budgets
  and WebP configuration/pixel validation. Add regression cases that reproduce
  the original failures before validating the repaired behavior.
- Reject invalid UTF-8 and unpaired surrogate escapes in JSON values and keys
  before Go decoding or marshaling. A shared standard-library-only internal
  helper covers raw JSON and bounded Go string traversal. Agent ToolCall/raw/
  ParsedArgs, the outer code parameter, native/MCP arguments, effective results,
  schemas, discovery and Store use the appropriate boundary checks. Actual
  native/MCP calls remain zero for invalid IDs; emoji and explicit U+FFFD retain
  their original values. Expose argument_invalid/result_rejected and the
  invalid_unicode machine reason without losing original Go causes.
- Convert Execute and AfterToolCall panics inside the common lifecycle to safe
  typed failures. Run after hooks, close scopes and finish child records; retain
  completed-write facts and original error-valued panic chains. Private panic
  text and unprocessed hook-failure output stay out of default events and JS.
- Correlate Go causes through a private WeakMap and the exact host Error identity
  plus VM-local sequence. Public callId changes, copied/replacement errors,
  prototypes and proxies cannot manufacture a canceled parent or another cause.
- Preserve trusted remote completion facts when a result is accompanied by an
  ordinary Go error. Check child arguments after parser/coercion/hook mutation
  before copies or execution. Bound partial failure output and generated error
  text from preparation, permission, execution, hooks and validators under one
  retained-payload budget. End events use the configured child summary limit.
- Use pure-Go golang.org/x/image/webp v0.46.0 configuration decoding, exact RIFF
  length checks and the existing 64 Mi-pixel limit. Verify its 2026-09-08 release,
  Go1.26 compatibility and GO-2026-5061 fix before adoption; retain the pinned
  QuickJS artifact/source/license bytes. Tidy both module graphs, including
  required x/sys v0.48.0 and the examples' resulting x/* selections; all selected
  changed x/* versions were released in August/September 2026 and require Go1.26.
- On the final native Windows Go1.26.4 snapshot, GOWORK=off and both live-test
  flags disabled: full root/examples ordinary tests, full race suites, vet,
  build and module verification pass. All 21 changed/new Go files pass gofmt
  checks; diff checks and pinned artifact/source/license test digests pass.
  The freshly compiled local MCP example returns 50 open issues and IDs 3/6/9
  in four host calls. Six original repository-external review probes also pass
  after recompilation against these fixes.
- Update package documentation and the existing draft PR. Cross-platform checks
  will run on the pushed repair commit; prior-head CI is not repair evidence.
  No live-provider or UI acceptance, version bump, tag, release or PR merge.

- The first repair commit's CI run 36966156132 passes the Linux repository job
  and Windows/Linux amd64/Linux arm64/macOS arm64 Codemode jobs. macOS Intel
  reaches the 10-minute Go test alarm in the pre-existing host-permit test.
  Its stack shows Run already trying to send its error while the test waits
  forever for host entry: the 40ms startup deadline does not guarantee Invoke
  was entered, and the unbuffered result channel hides that early termination.
- Make the host-permit fixture synchronize on actual host entry and second-call
  admission, then cancel explicitly. Check the occupied global permit, queued
  cancellation and retirement before Close's outstanding report. Every wait is
  bounded; buffered result channels and idempotent cleanup release callbacks on
  failures. Add an expired-startup-deadline case proving zero host dispatch.
  This changes tests only, without runtime timeout or retry-policy changes.
- The compiled native Windows race test executable passes both cases for ten
  iterations. Final root ordinary and full race suites pass again; Codemode vet,
  formatting and diff checks pass. The examples module is unchanged after its
  previously passing full ordinary/race/vet/build checks. Push the fixture repair
  so the final head can run all six remote checks again.

## 2026-10-02: Pi 1.0 Codemode feedback and lazy child contracts

- Follow the exact upstream v1.0.0 behavior reference, not the article's claims.
  Implement the first six approved follow-ups as three buildable review units;
  keep durable branch persistence and model/provider expansion outside scope.
- Add bounded syntax/source diagnostics, allowed-name suggestions, namespace and
  result documentation, shared BM25 search, and effective output budget reserves.
  Explicit safe public errors remain separate from original host Go causes.
- Add generic ResolveChildTools in Agent after approval/permission, validate and
  freeze its leaves, then check permission again. Reject nested containers and
  checkpointed containers; keep MCP and JavaScript out of Agent core.
- Add NewDynamic with source/header validation before discovery, one absolute
  setup/execution deadline, fresh executable bindings and a shared private
  invocation store. No nested suspension recovery or script replay is introduced.
- Make exceptional normalized/long MCP namespace aliases unambiguous with a
  namespace hash. Ordinary short aliases remain unchanged; no legacy shim.
- Reproduce wazero v1.12.0's cold-constructor version-cache race and serialize
  only runtime construction. Preserve parallel compilation/execution and the
  pinned WASM artifact. The cold eight-workflow race regression passes; upstream
  issue 2532/fix 2536 agree with the root cause, but the release lacks the fix.
- Native Windows Go1.26.4 ordinary root/example tests, vet and builds pass with
  GOWORK=off and live-test flags disabled. Focused Agent/sandbox/binding race
  checks pass; final combined full-race evidence belongs to the integration entry.

## 2026-10-02: Managed MCP wire, connection and OAuth boundaries

- Add the standalone mcp package using official SDK v1.8.0. Observe actual
  JSON/SSE/stdio frames, associate concurrent RPC IDs with exact SDK objects,
  reuse cache observations, bound retention, and reject missing raw data.
  Notification invalidation rejects late old responses before SDK cache entry.
- Freeze catalog identity/epoch, connection generation, revision, raw-page digest
  and exact schemas together. Walk SDK pages for TTL and notification freshness.
  Restore structured values with json.Number; reject unsafe numeric schema
  constraints before float conversion, while retaining ordinary decimal schemas.
- Own explicit HTTP/stdio connection attempts, bounded cleanup and identity/policy
  retirement. Windows attaches a suspended hidden process to a kill-on-close Job;
  Unix uses process groups. Tests cover process-tree exit and repeated cleanup.
- Repair Linux process-lifetime assertions after CI exposed inconsistent death
  observations. The old probe could report alive when /proc disappeared after
  kill(0), omitted X/x death states, and probed a bare PID again after termination.
  Bind observations to the captured starttime, classify Z/X/x and ENOENT/ESRCH,
  retain other observation failures and keep a confirmed termination permanent.
  The original random CI failure did not recur in 500 native Linux iterations;
  the exit/reap observation window is reproduced with an actual child process.
  Repaired native Linux targeted race tests pass 100 iterations and full MCP race
  passes three iterations; Windows targeted tests pass 20 iterations, targeted
  race passes three, and full MCP race/vet pass. Darwin/Linux arm64 test packages
  compile. Production cleanup and the three-second death deadline are unchanged.
- Fix a reproduced Windows cross-process CAS failure: CREATE_NEW could return
  AccessDenied during lock-file deletion. Load already held the common lock;
  replace its file-handle lifecycle with an atomic directory lock. Keep bounded
  contention waiting and reject actual permission errors without retry. Retain
  original storage/unlock causes behind safe error text. CAS pressure passes
  500 iterations (2000 child processes); related race checks pass ten iterations,
  and complete MCP ordinary/race tests pass. Explicit crash recovery still belongs
  to the host. Native MCP race contracts join the managed PR's platform matrix.
- Guard tool sends at physical handoff after host permission and token retrieval.
  Count accepted handoffs without claiming socket delivery, preserve trusted SDK
  terminal/input-required facts, and block duplicate sends including OAuth replay.
  Failed connections require explicit reconnect; scripts are never repeated.
- Add explicit host OAuth, token-only tool execution and true version CAS stores.
  Bind credentials to server/URL/identity/issuer/client; persist registration and
  scopes, pin refresh authentication style, retire changed/revoked authorization,
  and reject stale login completion. File storage uses a host-owned private
  directory, exclusive writer lock, synced replacement and explicit crash recovery.
- Keep all existing dependency versions. MCP SDK and wazero are current released
  versions as checked on 2026-10-02. x/oauth2 v0.35.0 is the SDK-selected minimum;
  the upstream is active through v0.37.0 on 2026-08-25. x/sys v0.48.0 was published
  2026-08-31. Existing OAuth/OS dependencies now become direct imports; no new
  framework or unpublished fork. Native Go1.26.4 build/race checks pass.
- Real loopback SDK HTTP, JSON/SSE/stdio, reordered responses, TTL/cache changes,
  OAuth state/issuer/refresh, final permission revocation and identity switches
  pass regression checks. MCP race tests pass for three consecutive iterations;
  full combined root/example race, ordinary, vet and build checks also pass.

## 2026-10-02: Dynamic MCP projection and bounded resources

- Add agent/toolset over host-owned Manager/Sandbox. Direct tools load initially;
  deferred/codemode tools connect lazily and load through bounded shared BM25
  search. Model search excludes already-declared leaves. Hidden leaves are absent
  from schemas/discovery and reject guessed invocation. Catalog publication uses
  one frozen snapshot plus final live-generation/revision checks.
- Keep live selections in bounded opaque host records tied to successful results
  on the exact transcript branch. Restore requires trusted saved identity/epoch;
  recreate executable definitions from the current catalog, never serialized
  executors or a shared SessionID. Source deadlines cover catalog setup and VM.
- Add resource/template list and read tools with explicit pagination cursors,
  scope/permission checks at dispatch and publication, and aggregate output caps.
  Preserve supported images into Codemode; validate image formats/pixels. Export
  arbitrary binary only via an explicit host ArtifactSink with inert generated
  names and scope/server/URI/MIME/hash descriptors. Paths and identities stay
  host-only; private export errors retain safe text and their original Go causes.
- Add examples/mcp-managed: real loopback HTTP SDK server, ordinary Agent runner,
  explicit permissions and deterministic model fixture. The compiled executable
  produces one bounded JSON summary: 50 open issues, titles 3/6/9, resource guide
  and hiddenAbsent=true, with three remote tool calls and one resource read.
- Fix the example's bounded shutdown by releasing its owned transport's idle
  connections after Manager cleanup. Synchronized eight-workflow tests reproduce
  the previous StateNew connection timeout and pass without larger timeouts.
- Native Windows Go1.26.4 final root/examples full ordinary and full race suites,
  vet, build and module verification pass, with GOWORK=off and both live flags off.
  All 50 changed/new Go files pass normalized gofmt checks; diff checks pass.
  Both separately compiled MCP examples execute successfully. The first two
  committed trees independently pass root/example test compilation, vet and build.
- Extend CI to native Windows amd64, Linux amd64/arm64, macOS amd64/arm64 MCP/race
  contracts and both compiled examples; allow stacked MCP PR base branches. Remote
  checks will report against pushed heads. Do not claim native Unix execution
  from Windows cross-compilation or count local checks as remote CI results.
- Update package/root documentation and the six-item acceptance table. No live
  model-provider call, frontend UI acceptance, durable branch store, version bump,
  tag, release or automatic PR merge. Review units are ordered by dependency.

## 2026-10-02: Resource directory identity and filesystem error repairs

- Initial PR18 CI on Windows/macOS exposes a test that compares directory strings
  after the implementation resolves aliases. Reproduce it on Windows by supplying
  an existing absolute directory with a trailing dot component. Compare actual
  filesystem identity with os.SameFile instead; keep filename, byte/hash, scope
  binding and private-path projection checks, including the resolved directory.
- Preserve FileArtifactStore root/open/write/sync/close causes with Go wrapping,
  include cleanup close/removal failures, and use io.ErrShortWrite explicitly.
  Original filesystem errors stay host-visible while safe Agent failures retain
  the completed remote read facts. Regressions prove os.ErrNotExist/os.ErrClosed
  identity and public redaction; the old missing-root behavior fails the test.
- Native Windows resource full race tests pass ten iterations; examples ordinary
  tests, vet and build pass. The root ordinary rerun separately exposes an
  intermittent cross-process credential-store CAS failure under investigation
  for the managed-connection review unit. No production path normalization or
  containment limit is weakened to make the directory assertion pass.

## 2026-10-02: Review remediation for MCP PR16, PR17 and PR18

- Apply valid source-header output caps before every post-parse pre-VM return.
  Replace the unreleased child-resolver return with ChildToolResolution, carrying
  one absolute deadline through catalog setup, permission rechecks, execution,
  child calls and after hooks. Keep MCP/JavaScript outside the Agent core.
- Bind Toolset exposure to the actual connection configuration, not an earlier
  Manager enumeration. Check the captured catalog at entry and at the physical
  HTTP/stdio handoff, including after waiting for approval and with no host hook.
  Preserve typed rejection reasons while transport facts remain authoritative.
- Serialize final credential CAS with epoch replacement; cancel old dispatch
  before CAS, then retire actual SDK sessions outside the lock, including on
  failed publication. Keep host callbacks/network login outside Manager locks.
  Persist a refresh claim before its POST so ambiguity/cancellation survives
  reconstruction and concurrent instances; successful refresh advances twice.
- Persist the SDK-validated resource indicator and registered DCR auth style.
  Preserve private authentication/storage causes under safe error text, including
  nonzero published CAS versions followed by cleanup errors. Resource is required
  in this unreleased credential structure: old development records require a
  host CAS tombstone before explicit login, without an inferred migration.
- Observe actual SDK-session/stdio-owner exit, reject old cached catalogs and
  require explicit reconnect. Drain stderr independently of exec.Cmd.Wait so
  inherited handles cannot conceal owner exit; retain exit/cleanup causes.
  Record ordinary successful SDK list responses as completed even when the raw
  snapshot is subsequently rejected by local invalidation.
- Correct Unix ownership to the assigned process group. Trusted servers must
  not escape it with setsid/setpgid/daemonization; those servers require host
  containment. Windows retains Job Object ownership. Real Linux tests prove this
  boundary and independently reclaim the deliberately detached fixture; they
  do not claim the Unix launcher can contain escaped descendants.
- Root and examples full ordinary/race suites, vet, build and module verification
  pass on Windows Go1.26.4, with GOWORK=off and both live flags off. The newly
  compiled managed example produces 50 open issues, titles 3/6/9, resource guide
  and hiddenAbsent=true. Process regressions pass three focused Windows race
  iterations; the complete MCP test binary also passes on native WSL2 Linux.
  All changed/new Go files pass formatting checks. No dependency/WASM upgrade,
  live-provider request, UI acceptance, release, tag or automatic merge.

## 2026-10-02: Second review fixes for MCP PR16, PR17 and PR18

- Bound dynamic pre-VM directory, invalid-catalog and final-permission failures
  with the effective source/configured diagnostic budget. Preserve private Go
  causes, failure classification and zero-dispatch facts, including resolver
  panic and cancellation. Replace the unreleased SourceLimits tuple with
  SourceLimits{Timeout, OutputBytes}; the generic child resolution contract adds
  FailureTextLimitBytes without introducing MCP/JavaScript into the Agent core.
- Fence terminal OAuth publication with synchronous connection stale/cancel
  before any refreshed token is visible. Keep external callbacks reentrant and
  lock-free, SDK Close asynchronous, and durable refresh claims unchanged.
  Separate mixed registration configuration keys from the SDK-resolved client;
  persist and restore actual CIMD fallback clients and authentication methods
  without re-registering or changing identity when AS capabilities change.
  Earlier mixed-strategy development keys require explicit login under the new
  derived key; no guessed migration or compatibility wrapper is introduced.
- Retain unsent RPC ID reservations through approval, and replace the maximum
  retired-ID watermark with bounded exact numeric ranges. Delayed requests
  remain valid, while actual replay and responses to unsent slots stay rejected.
  Each MaxTombstones bound applies to recent IDs and compact ranges separately;
  range/string exhaustion fails closed instead of losing replay protection.
- Return real unsent stdio rejection errors while cancelling only the logical
  SDK child context. Clear the unsent frame so later and concurrent calls on
  the same session work; partial/physical write failures remain terminal, with
  no fabricated send success, retry or automatic reconnect.
- Revalidate search identity, connection generation and catalog after final
  approval and before selection publication. Server/tool hiding and directory
  notifications reject old metadata without consuming the selection budget;
  fresh searches retain transcript-branch isolation.
- Root-cause macOS reconnect EPERM in XNU's zombie-only group signal handling.
  Inspect the exact process group only after EPERM; exclusively zombie/absent
  members mean cleanup is complete, while live members and inspection failures
  preserve the error. Add a native Darwin zombie-group regression and retain
  the existing supported descendant and reconnect contracts.
- Preserve the first physical stdio write cause before the SDK closes the
  transport. Reader-side close errors retain that cause and do not cancel the
  logical context, so fatal IO still retires the session instead of being
  mistaken for an unsent denial. A controlled real-SDK close ordering reproduces
  the lost-cause failure on both zero-byte and partial writes without scheduler
  retries; original call and session errors must retain the physical cause.
- Permanent regressions reproduce the reviewed OAuth defects on the old
  implementation and pass on the repair. After the final IO-cause correction,
  root/examples full ordinary/race suites, vet, build, module verification,
  formatting and diff checks pass on Windows Go1.26.4. The new PR17 parent also
  independently passes all-package ordinary tests, MCP race, vet and build.
  Native WSL2 Linux passes the complete freshly compiled MCP binary, controlled
  physical-write regressions 100 times, and all wire tests three times. Native
  macOS ARM/Intel CI passed MCP race and process/reconnect regressions on the
  first repaired PR17 head, confirming the Darwin cleanup fix; the final branches
  use the existing five-platform CI matrix. Newly compiled local examples return
  50 open issues, titles 3/6/9, the guide and hiddenAbsent=true. No dependency/WASM
  upgrade, UI/live-provider acceptance, release, tag or merge is performed.
  Keep all three existing PRs layered.

## 2026-10-02: Final review repairs for MCP delivery

- Return conservative host/RunOptions diagnostic bounds even when source options
  are invalid, and carry them through dynamic child resolution before returning
  the error. No directory or VM runs for rejected source. Regress malformed,
  invalid and oversized source through both RunToolCall and the next model
  request, including an eight-byte host ceiling and a one-token option.
- Share only the pinned QuickJS compilation cache for the process lifetime;
  serialize cold compilation, while runtime imports, memory limits, VM state,
  cancellation and Close remain independent. The macOS Intel CI timeout occurred
  after ten cumulative minutes in the codemodetool package, with its stack in
  repeated WASM compilation. Do not raise timeouts or weaken assertions. A local
  five-creation benchmark, including the initial cold compile, changes from
  257 ms/op to 56 ms/op; warmed creation is about 9 ms/op. Runtime isolation,
  active-script cancellation, concurrent creation and linear memory caps pass
  repeated Windows race tests.
- Preserve the OAuth authorization endpoint's fixed query parameters, including
  encoded, empty and repeated values, while requiring an unambiguous SDK-owned
  authorization-code/PKCE query. Reject fixed collisions and duplicated OAuth
  parameters before the host callback or token exchange. Real SDK control cases
  that failed on the old wrapper now pass; no dependency or public API change.
- Retain a POST SSE body's original non-EOF IO error in its own RPC slot before
  the SDK replaces it with a generic termination error. Track whether that
  body's matching result/error has arrived, so normal post-response cancellation
  and drain cannot overwrite a completed RPC. GET/EOF handling keeps the SDK's
  existing stream semantics. Real SDK/server controls preserve the error chain,
  one actual side effect, concurrent/next calls and completed-response results.
- After every waiting resource approval, recheck the actual scope, connection
  and exposure before publication or ArtifactSink handoff. Rejection withholds
  old text/structured data but retains completed remote-read facts. Regress real
  Agent reads/lists/templates, sink side effects, identity/exposure changes,
  reconnect, ABA and cancellation.
- Capture a Manager configuration snapshot that includes every server slot,
  including cold servers. Revalidate it before publishing the tool directory so
  hidden names cannot survive in resource schemas. Snapshot accessors detach
  configuration; slot replacement detects exposure/rule changes and ABA. An
  in-flight configuration change rejects the old directory; a fresh explicit
  Resolve uses the new policy without replaying any script.
- The completed repair tree passes root/examples full ordinary and race tests,
  vet, build, module verification, changed-Go formatting and diff checks on
  Windows Go1.26.4, with GOWORK=off and live flags off. Newly built local examples
  report 50 open issues, titles 3/6/9, the resource guide and hiddenAbsent=true.
  OAuth and wire targeted race regressions also pass three iterations. Keep the
  existing five-platform CI matrix for native validation of the updated PRs.
  No dependency/WASM upgrade, UI/live-provider check, release, tag or merge.

## 2026-10-02: Merge the MCP stack and prepare v0.12.0

- The user explicitly authorized merging PR16, PR17 and PR18 and publishing a
  version. Merge each exact reviewed head with a merge commit, retargeting the
  next PR to master only after its parent is merged. PR16 merges as 72068cd,
  PR17 as 3f08677 and PR18 as b2d12c7; all reviewed heads remain ancestors.
- Verify the final master tree equals reviewed head 0b895d7. All 12/18/18 PR
  checks succeeded, including the five native platforms and their duplicate
  PR-triggered checks. PR17's macOS Intel codemodetool tests complete in 25.572s.
  Root/examples ordinary and race tests, vet, builds and module checks from the
  completed repair remain applicable to this unchanged implementation.
- Use the already reserved source version 0.12.0, following the latest published
  v0.11.2. The release includes the core transcript/provider updates and restored
  Token Harbor support already on master, the optional MCP adapter, bounded
  Codemode, managed MCP connections/OAuth and dynamic tool/resource projection.
  Release notes must identify the Agent API migration and Go1.26.2 minimum.
- Correct the source-rejection diagnostic and shared compilation-cache docs to
  match the final implementation. These release-preparation changes are prose
  only; no additional dependency or API change is introduced.
- Package the clean release commit as Windows amd64, Linux amd64/arm64 and macOS
  amd64/arm64 CLI archives with README.md and SHA256SUMS.txt. Verify embedded VCS
  identity, executable archive modes, downloaded asset hashes, annotated tag and
  published Go-module resolution before reporting publication complete.

## 2026-10-03: Preserve stateful OAuth reconnect after access-token expiry

- Reproduce PR20's regression: retiring a stateful session with an expired access
  token returned context cancellation from the SDK's DELETE preparation, so
  Manager.Reconnect stopped before establishing its replacement despite a usable
  refresh credential. Both cached-only and persisted-expiry regressions fail on
  the original implementation and pass after the repair.
- Distinguish retirement before any refresh claim or token request with a private
  error type. Connection.Close recognizes only that exact skipped-DELETE outcome
  as normal local teardown. Old sessions never refresh; the replacement refreshes
  an expired persisted credential once, after old-session teardown completes.
- Preserve actual DELETE cancellation, deadlines and transport failures, including
  on repeated Close. Interrupted refreshes retain their durable pending claim and
  require explicit authentication; retired connections still cannot dispatch tools.
- Update the OAuth lifecycle documentation and stateful fixture to check physical
  DELETE attempts and accept rotated access tokens. Focused lifecycle regressions,
  all MCP tests with the race detector, go vet ./mcp, formatting and diff checks
  pass on Windows Go1.26.4. Validation is limited to MCP and local SDK fixtures;
  no dependency or public API change. The user subsequently authorized committing
  this repair and pushing master to GitHub; no release is requested.

## 2026-10-03: Publish v0.12.1 maintenance release

- The user requested a patch release for the fixes since v0.12.0. Advance the
  package version and examples dependency to 0.12.1, including all eight fix/test
  commits in PR19, PR20 and the expired-token reconnect repair on master.
- Release notes cover SDK-aligned wire decoding and cache isolation, result
  validator panic containment, OAuth callback retirement, stable client identity
  through secret rotation, refresh cancellation and stateful reconnect. Document
  the one-time explicit login for old mixed-strategy confidential-client keys.
- Root and examples all-package race tests, vet, module verification and tidy
  checks pass on Windows Go1.26.4 with workspace mode and live tests disabled.
  Tidy initially reported only checkout CRLF in go.sum; normalize those working
  file line endings without changing dependency versions or checksums. The
  implementation at 3517a6c also passed the complete five-platform native CI.
- Publish an annotated v0.12.1 tag and formal GitHub Release from the clean version
  commit, with Windows amd64, Linux amd64/arm64 and macOS amd64/arm64 CLI archives
  plus SHA256SUMS.txt. Verify embedded revisions, Unix executable modes, downloaded
  hashes and public Go-module resolution. No provider live test or API change.

## 2026-10-03: Analyze and order the next Pi 1.0 follow-up

- Compare v0.12.1/master dcdcf9b with exact upstream Pi v1.0.0 a13d35a and screen
  the 25 later commits through a276dabe. Record shipped items 1–6 and the next
  implementation sequence in docs/pi-1-followup.md; point the historical design
  and documentation index at that current plan.
- Identify the actual persistence gaps: invocation-only Codemode state, no
  conditional branch batch in session Storage, repository-local writer claims,
  and no shared finalization boundary for store writes and accepted tool results.
  Reuse lane sequence and existing trusted tool restoration instead of duplicating
  stores, runtime loops or loaded-tool authority. Require crash/unknown-effect
  handling without script replay, and retain the checkpoint restrictions.
- Schedule narrow applicability regressions for the existing Codex callback-bind
  fallback and the missing capacity-error phrase before the storage -> Codemode
  state -> tool-selection/compaction sequence. These are source-grounded candidate
  defects, not newly reproduced failures. Separate grammar from dynamic tool
  anchoring; defer broader OAuth, images, classifiers and interactive continuation
  behind concrete host/provider requirements.
- Recheck current CI evidence and MCP SDK/wazero/x/sys maintenance. No dependency
  change, source-code change, test execution, live provider call or UI acceptance
  is part of this planning update. Validate document links, plan consistency and
  git diff whitespace; do not create implementation chats or publish changes.

## 2026-10-03: Implement Pi 1.0 first-round follow-up (N0a/N0b/N1a–N1c)

- Baseline: master dcdcf9b740190f20ace5d8bbad7a9670fe2eafd1 / v0.12.1.
  Execute the user's approved first round; preserve the earlier planning edits.
  No dependency/version update, commit, push, tag or release.
- Reproduce callback-bind/cancellation and capacity classification failures before
  fixing them. Codex login stops before URL/browser presentation when binding
  fails, retains the network cause, and never prompts after cancellation.
  Verify loopback callback, ordinary manual code, mismatched state, cancellation
  before login/on auth/while waiting/during prompting. Capacity errors use the
  existing retry budget only before output; zero-retry and quota rules remain.
- Add Storage.ReadBranch/CompareAppend with session/lane/Seq/head CAS and one
  physical JSONL v2 batch. Preserve unrelated-lane commits and reject head ABA.
  Writable handles lock the actual file before replay/repair, using existing
  x/sys (Windows reserved byte lock / Unix flock). Failed writes poison the
  handle. Version 1 is rejected, never silently migrated. The Storage interface
  and JSONL header change are intentional API/format breaks.
- Add the generic Agent RunJournal and sequential top-level ToolState capability.
  Agent imports no session/MCP/Codemode implementation. Session.PrepareRun owns
  canonical transcript and exact-version commits while invoking the existing
  Runner. Save tool intent before dispatch; atomically save final results,
  runtime-owned child facts and accepted state after parent hooks/validation.
  Canceled finalization records failure rather than success with discarded state.
  Failed commit stops the next model turn; Wait retains observed results.
- Codemode durable mode requires an explicit host StateNamespace, restores a
  disposable bounded store and stages only data. Versioned envelopes preserve
  original JSON value lexemes, including escaped Unicode/HTML and Number limits.
  Retain invocation-only behavior when no journal is installed. Check identity
  and auth epoch on every load/commit, including reads from accepted batch state.
- Restore from the actual branch, outside compaction's model window. Fork/move
  remains isolated; a fresh host provenance token drives existing toolset
  restoration, which rebuilds current executors and honors hidden/revoked rules.
  A pending intent returns ErrInterrupted; no retry, automatic script replay,
  JS stack restoration or expanded checkpoint support is introduced.
- Expand examples/mcp-managed to save/compact then resume in a second process,
  using a fresh MCP connection, saved small JSON, restored selection and a
  reauthorized resource reference. Default mode uses a temporary session.
  Update package/example docs and the current follow-up plan.
- Local verification (Windows amd64, Go 1.26.4, GOWORK=off): root and examples
  full tests; race for agent/... (including session/checkpoint/toolset),
  codemode/..., internal/cli and pkg/pigo; managed-example race including the
  separate-process save/resume test; root/examples vet, build and module verify.
  Focused checks were repeated after final state-envelope/cancellation fixes.
- Process tests cover writer aliases/competing handles/process death, batch
  failure before write/partial/no-newline/flush/sync/applied, and death after a
  side effect but before finalization. Hook/validator/cancel/sync failure tests
  retain child facts without committing rejected state. Full root and examples
  module files/checksums are unchanged.
- Linux amd64/arm64 and macOS amd64/arm64 session/Codemode packages cross-compile.
  Extend the existing five-native-platform CI matrix with session race/fault
  tests and the managed restart regression; it has NOT run for this unpushed
  working tree. Local process tests do not simulate power loss. No live model
  provider request, OAuth browser UI or computer-use acceptance was performed.

## 2026-10-03: Review and preserve the existing local first-round work

- Review the existing 26 tracked changes and 11 new files against dcdcf9b in
  an independent clone on codex/mcp-local-review-20261003. Verify all source
  file hashes before and after copying; leave the original development tree
  untouched because other Codex processes may still be active. The implementation
  above predates this review; this pass adds focused corrections and regression
  evidence, not a replacement implementation.
- Reproduce and fix a journal prefix conflict when a later run first introduces
  a system prompt or static tools after plain conversation. Journaled initialization
  now appends the system declaration; ordinary runtime initialization is unchanged.
- Reproduce and fix cancellation inside the OAuth fallback-presentation callback:
  check cancellation before entering the manual prompt. Add a complete JSONL v1
  fixture and verify rejected headers leave the original bytes untouched.
- Reproduce all four findings from the automated full review, then fix the
  ownership boundaries: persist the basic child ledger outside message JSON;
  enforce state limits after JSON escaping; finalize aborted assistant/tool
  pairs despite cancellation (also at stream completion); and validate batch
  duplicates/parent links using the normalized IDs that storage applies.
  Exercise rejected and successful calls after JSONL close/reopen, per-value
  and total escaped-state limits, and both memory and JSONL transaction stores.
  Optional child output/error details remain excluded from durable runtime records.
- Follow-up review adds regressions for gated cancellation, including a suspended
  sibling, assistant-ended Continue with newly introduced system context, and
  separately invalid UTF-8 tool/namespace strings whose concatenation is valid.
  Settle canceled gated calls without dispatch, validate the original Continue
  tail, and check both state-key strings independently before committing.
- The final targeted review identified approval waits that return cancellation
  as a gate error. Reproduce the failure in sequential and parallel execution,
  then settle that journaled preflight through rejected outcomes as well. Keep
  the existing unjournaled gate-error/cancellation behavior. Per the user's
  closeout instruction, validate this last correction with tests and direct
  control-flow review instead of starting another automated review process.
- Verify root/examples full tests, vet, build and module integrity on Windows amd64
  with Go 1.26.4. Run race tests for agent/..., codemode/..., mcp, internal/cli and
  pkg/pigo, plus the managed example's separate-process restoration. Repeat the
  affected agent and CLI race tests after the fixes. Compile and run both MCP
  executables, including explicit managed save/resume processes.
- Keep dependency and release versions unchanged. Check changed paths and added
  content for credentials, private keys, personal home paths and unrelated files.
  User authorization covers focused commits and pushing this independent branch;
  it does not include merging or publishing a release. Native multi-platform CI
  must be checked against the pushed HEAD. No live provider or OAuth UI test.

## 2026-10-03: Final review of PR #21 before merge and v0.13.0

- Fetch remote master/tags and confirm #21 at 5d6452104df61276a84281094dd137b45437effd
  is the only open PR; #19/#20 are already in v0.12.1. Preserve the older dirty
  checkout in the named before-pr21-update-20261003-first-round-local-backup
  stash, then review the exact remote implementation on a local release branch.
  The user now explicitly authorizes updating, fixing, merging and publishing.
- Reproduce one remaining journal failure: a non-cancellation approval service
  error leaves the whole batch unexecuted but without matching tool results,
  so the next PrepareRun rejects the session as interrupted. Four regressions
  fail before repair (sequential/parallel, prior allow/suspend decisions).
- On journaled preflight failure, stop requesting approvals, reject every call
  as not-started/not-dispatched, preserve the original error for Wait and commit
  the complete failed turn. Do not dispatch previously allowed siblings, replay
  scripts or change ordinary unjournaled gate-error behavior.
- Root/examples all-package race tests, vet, builds and module verification pass
  on the reviewed baseline. Agent/session/checkpoint/Codemode race tests pass
  after this repair. Check the repaired exact HEAD's five-platform native CI
  before merging; the previous PR CI cannot certify this new correction.
- Prepare v0.13.0 as a minor release because session Storage and JSONL v2 change
  the public contract. Keep dependencies and live-provider/UI testing unchanged.

## 2026-10-03: Merge PR #21 and prepare v0.13.0

- Merge the repaired PR head 992c767534ef51fbac5db3b211de5b6650653cfa only after
  all six jobs pass in both PR CI run 37108407584 and push run 37108404659.
  Native Windows amd64, Linux amd64/arm64 and macOS amd64/arm64 cover the new
  session fault/lock tests, Codemode/MCP race and managed process restart.
- Fast-forward local master to merge 1ad122443b98381681422b5ca6ff31cbba0cdf7c.
  Retain the named pre-update stash as a recovery copy. Bump pigo.Version and
  the examples module requirement to v0.13.0; no third-party dependency changes.
  Update the follow-up table to record N0a/N0b/N1a-N1c as merged and verified.
- Root/examples module verification and tidy checks pass. Windows checkout
  CRLF in go.sum was normalized only while checking tidy, then restored after
  confirming checksum contents match HEAD. Repeat focused version/CLI tests
  and example compilation for release preparation; final master CI remains
  a publication gate for the exact release commit.
- Publish an annotated v0.13.0 tag and formal GitHub Release from a clean source
  commit, with five CGO-disabled CLI archives and SHA256SUMS.txt. Require matching
  embedded source revisions, clean-tree build flags, archive members, Unix
  executable modes and Windows CLI version; verify uploaded downloads before
  closeout. Keep release evidence with the ignored local dist artifacts.
- Release notes explicitly identify the Storage API and JSONL v2 break, absence
  of automatic history migration, and host reconciliation for interrupted tool
  effects. No live provider request, OAuth UI or power-loss acceptance is claimed.
