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

## 2026-10-03: N2 native custom inputs, first checkpoint

- Fetch confirms `origin/master` remains `6ab184a8e411644206a83cba0a68a8328d18b536`
  (v0.13.0), including PR #21; no newer implementation is duplicated. Root and
  Agent instructions were read. This checkout has no `.agents/skills` directory.
- Add opt-in text/grammar sampling with explicit prefer/require policy, exact
  model-only capabilities, pre-request rejection/fallback and single-string
  argument mapping. Only OpenAI `gpt-6-astra` is enabled from current official
  examples; Codex and sibling IDs remain unknown. Sources: `docs/native-tools.md`.
- Responses custom declaration, delta/done/terminal, ctc IDs and replay are wired
  through SSE and the shared Codex WebSocket codec; Agent/Codemode keep their
  object arguments. Bounded input, completion consistency and identity checks
  reject unsafe partial streams. Metadata is cloned and retained in transcripts.
- Go 1.26.2 on cloud Linux: existing pigo and Agent tests pass. New native
  capability, fallback, replay, cancellation, malformed stream and resource
  tests pass, including 21 cases across all three local transports. A public Go
  proxy 403 for the pinned encoding module was resolved by direct source fetch
  of the same version; no dependency version changed. Full validation and the
  separate review pass are still pending at this checkpoint.

## 2026-10-03: N2 execution, replay and local validation checkpoint

- Add eight actual Agent/Codemode loopback scenarios: native execution and next
  request replay, leaf failure, partial stream, cancellation, source limit,
  ordinary function compatibility, unsupported-model fallback and dynamic
  Codemode binding. Session snapshots detach the added sampling metadata.
- Separate diff review found index rebinding and a native declaration accepting
  function output. New regressions failed on all three transports before repair;
  calls now retain their index and cannot change their declared wire type.
  Reject unknown namespaces and invalid Unicode instead of changing code while
  decoding. Ordinary function calls retain their existing path.
- A new serialization round-trip regression failed because the dedicated pigo
  Context codec omitted sampling. Fix all top-level and system-message encoder
  and decoder paths; the regression now passes. Require-mode tests additionally
  prove zero authorization, payload-hook and HTTP calls on unsupported models.
- Local Linux Go 1.26.2: root and examples `go test -count=1 -race ./...`,
  `go vet ./...`, `go build ./...`, `go mod verify` and `go mod tidy -diff` pass.
  After the serialization/namespace corrections, pigo, Agent, Codemode adapter
  and session race tests pass again; affected vet/root build pass. Compiled
  `mcp-codemode` and `mcp-managed` local fixtures both run successfully.
- Only synthetic local provider fixtures ran. Dependency versions, release
  metadata and downstream pins are unchanged. Independent second-reviewer
  acceptance and the draft PR's native CI remain separate gates; N3 has not
  started and does not share this implementation branch.

- Final retry review reproduced malformed terminal custom output named
  `model is at capacity` causing three Responses SSE requests/four Codex SSE
  requests. Record custom output before validation and close the pre-output
  retry window; both now make one request. Declaration/history encoders and
  decoder also consume one resolved mapping, with a catalog-refresh regression
  ensuring new requests observe revoked capability without changing an already
  selected representation. Draft PR: https://github.com/Icatme/pi-go/pull/22.
- After those final retry/mapping changes, root and examples full race, vet and
  build checks pass again. The earlier draft head `63ed143` passed all six CI
  jobs in run 37119336354, including five native platforms; the new commit must
  receive its own CI result before that evidence is treated as final.

## 2026-10-03: N3 design checkpoint after N2 acceptance

- N2 exact head `28f7ade22d1080fa892321ed686e752441664ee4` passed independent
  cloud review with no confirmed findings and all six jobs in CI run
  37119799205. PR #22 remains unmerged. Remote master is still `6ab184a8`.
- Create `codex/n3-tool-anchors` from that head. Select only the Responses
  `additional_tools` protocol and exact OpenAI `gpt-6-astra` model, with official
  sources and replay/fallback boundaries in `dynamic-tool-anchors.md`.
- Reuse system-message tool changes and the existing Agent resolver. Anchor
  additions only; removal/redefinition, unsupported models or unsupported
  native additions fold to the current tool list before dispatch. Prefix
  stability is a serialization property, not measured provider-cache behavior.
- This checkpoint changes documentation only. N3 implementation and validation
  are pending. No live provider calls, dependency or release changes.

## 2026-10-03: N3 implementation checkpoint

- Add exact model-only ToolAdditions capability and additive-only Responses
  request planning. Initial declarations and input prefixes remain stable;
  deletions/redeclarations, unsupported targets, dynamic native custom tools and
  native resource bounds fold to the current list before sending.
- Carry namespace replay metadata through pigo, Agent, JSON, cloning and pending
  call binding. Only anchored requests enable the new namespace checks; other
  Responses/Codex function paths retain their prior behavior. Foreign namespaces
  and changed namespace/name identities fail, including conflicting terminal
  items. Malformed anchored output cannot reopen the retry window.
- A focused replay test first failed on dropped namespace metadata. A loopback
  test also reproduced foreign namespaces being accepted as a local short name.
  Both now pass. Real Agent execution additionally reproduced existing empty
  argument objects being cloned into null; preserve empty pigo maps so no-argument
  functions still reach their existing object-schema validator as objects.
- Focused pigo/Agent tests pass, including five actual Agent resolver/execution/
  restoration scenarios and seven stream/HTTP boundary scenarios. Dynamic
  native-model history plus previous_response_id fails before payload/auth/HTTP;
  server-side tool revocation is not guessed. Full root/examples checks and
  independent review are the next gates.
- Existing Actions filters do not cover a PR based on codex/n2-native-grammar
  or pushes to this N3 branch. CI is not claimed; the workflow is unchanged.

## 2026-10-03: N3 draft and local validation handoff

- Draft PR #23 targets codex/n2-native-grammar, exact N2 base 28f7ade, and carries
  only the N3 delta. Independent N3 review is pending; no PR is merged.
- Root and examples full `go test -count=1 -race ./...`, `go vet ./...`,
  `go build ./...`, `go mod verify` and `go mod tidy -diff` passed on cloud Linux
  Go 1.26.2 with live tests disabled. Module files remain unchanged.
- Final diff review reproduced a changed terminal call_id under the same item
  ID being treated as a second invocation. Reject item/call identity reuse in
  anchored responses and bound generated function calls to 128. Both added
  regressions first failed, then passed; pigo and Agent full race/vet and root
  build passed again after the fix. Nine stream/HTTP cases now cover the native
  boundary. Dropping incompatible namespace metadata also drops its provider
  item ID; affected native/custom/sampling race cases, vet and root build pass.
- Actions API confirms no PR workflow runs for the pushed implementation head;
  existing base/push filters explain the absence. Native multi-platform CI and
  live-provider/cache acceptance are not claimed. Final scope and evidence are
  recorded in dynamic-tool-anchors.md.

## 2026-10-03: N3 independent-review P2 repair

- Reproduce the review finding at exact head ca890265: explicitly disabling
  SupportsMidConvoSystemMessages returns from tool planning before setting the
  exact native model's full-history requirement. Add/remove/redefine and
  remove-and-replace histories incorrectly accept previous_response_id; four
  new cases failed and reached a fake HTTP transport once each.
- Determine the registered native capability and dynamic-history requirement
  before selecting folded system-message fallback. The eight true/false flag
  matrix cases now reject stateful dynamic replay before payload/HTTP, while
  stateless folded requests and static-tool previous-response replay remain
  valid. A registered local authorization fixture additionally proves zero
  authorization, payload and HTTP calls on rejection.
- Focused regressions and full pigo/Agent race tests pass. Root vet/build and
  examples full race/vet/build also pass after this correction. No real provider
  call, merge or release occurred; the repaired head awaits independent recheck.

## 2026-10-03: v1.0.1 focused compatibility follow-up

- Fresh remote master is c29b4d502abb4ceb98c464db9918e0b8f7263615: N2/N3 are
  merged via #22/#23 after independent review, and post-merge CI run 37126745895
  passed all six jobs. No open PR or newer competing implementation was found.
- Upstream latest stable is v1.0.1 at a7229ddc21810d6245105978033b7df645ecc2f7.
  Compare its actual source with Go instead of redoing journal, branches,
  grammar, additional_tools, capacity retries or Codemode output limits.
- Prioritize the existing IsContextOverflow host API's missing gateway error
  forms; initially list HTTP error-body bounds for follow-up, then withdraw
  that candidate after the complete transport audit recorded below.
  Four narrowly scoped expressions cover LiteLLM/OpenAI-compatible maximum
  context lengths, Poolside, Together and DS4. Do not add automatic compaction,
  retries, credentials, models or dependencies. Qwen's input-range message is
  ambiguous with empty input and is intentionally not classified as overflow.
- Before repair, all seven new explicit-error examples and fourteen local HTTP
  Completions/Responses cases failed. Regressions also preserve case-insensitive
  matching, rate/throttle exclusions, error-only classification and rejection
  of unrelated or ambiguous text. No live provider request is used.
- Focused overflow race tests and root/examples full `go test -count=1 -race
  ./...`, `go vet ./...`, `go build ./...`, `go mod verify` and `go mod tidy
  -diff` passed on cloud Linux Go 1.26.2. No module file changed. Independent
  read-only review found no correctness or regression issue and separately
  passed the focused race tests and diff check. Push/PR CI is recorded on the
  draft PR; no native-platform or live-provider result is inferred from local
  Linux tests. No release, merge or downstream dependency change is authorized
  in this follow-up.

## 2026-10-03: shared HTTP error-body candidate closed after audit

- The proposed resource defect is not present. `HTTPStreamClient.postStream`
  first calls `providerHTTPClient`; `Stream` and `StreamSimple` also install
  that transport before provider dispatch. The shared path covers direct
  PostStream consumers, OpenAI-compatible Completions and Codex SSE; public
  Complete entry points and the Responses SDK are already tested as bounded.
- `providerHTTPTransport.RoundTrip` reads at most the configured limit plus one,
  defaults to 64 KiB, closes the source and replays only a bounded prefix.
  Existing wrapped clients retain their host-configured policy. Redirect bodies
  remain lazy and bounded. Downstream `io.ReadAll` does not imply an unbounded
  upstream read here; the earlier candidate missed this layer.
- Existing HTTP/shared-stream/SDK/redirect race tests pass, including source
  byte counts and Close, truncation metadata, original read failures,
  cancellation, connection reuse and untouched successful streams. A temporary
  local overlay additionally exercises the shared PostStream entry directly:
  default retains 65536 bytes after reading 65537; an explicit 32-byte policy
  retains 32 after reading 33; both close the original body and make one request.
  A synthetic tail beyond the limit never reaches the reported error.
- Observation identifiers reject overlong or invalid characters. Raw error text
  and error chains intentionally retain provider content; this is not a promise
  of automatic body redaction. All fixtures are synthetic and local.
- Read-error preservation is verified at the transport/SDK boundary; the shared
  PostStream parser still discards the error returned while reading that bounded
  body. This audit does not claim universal end-to-end error-chain preservation.
- Withdraw the candidate and correct the plan in PR #24. No additional runtime
  implementation or separate feature PR is needed. Independent read-only audit
  found no relevant bound bypass, reviewed the corrected documentation and
  passed `git diff --check`.

## 2026-10-04: preserve dynamic declarations after failed-history filtering

- Fixed the reviewed Responses regression where an error/aborted assistant at
  the beginning of history was filtered out, moving a later system declaration
  to index zero. The tool plan treated it as dynamic while the encoder treated
  it as initial, dropping the tool from both declaration locations.
- Record initial-system identity after protocol-specific system folding and
  before message transformation. Use that identity for tool anchoring, initial
  prompt exclusion and system-update rendering. This also preserves later
  section updates on Responses and Codex when failed history is removed.
- Added public CompleteSimple loopback request regressions for error/aborted
  history, normal completion and genuine initial tools. Assert the declaration
  occurs once before its user message and preserves namespace/item-ID replay.
  Separate Responses/Codex cases cover section update and removal semantics.
  The affected cases failed before the fix and pass after it.
- Focused anchor/system regressions, root `go test -race ./... -count=1`,
  `go vet ./...` and independent read-only review passed on Windows Go 1.26.4.
  No live provider call, dependency change, commit or push was made.


## 2026-10-04: Responses request context preparation (stage 1)

- Isolated worktree: `C:/Users/icatm/.codex/worktrees/responses-context-stage1/pi-go`,
  branch `codex/responses-context-stage1`, base/HEAD
  `03237e67c284f9d4d8ef55478e5a95f7524da7c5`; local uncommitted review only.
- Prerequisite: precisely transplanted only the existing `hadInitialSystem` fix
  and its two regressions from the dirty `V:/gitdownload/pi-go` workspace owned
  by chat `01a10555-6b07-73b3-bbe6-3c2db33672af`. Its worklog was not copied.
  Preserve this prerequisite separately from the behavior-preserving refactor.
  Original three dirty files were verified byte-for-byte unchanged.
- The builders previously repeated context normalization, while tool planning
  needs pre-folding system updates and wire encoding needs resolved history.
  Responses now normalizes once, plans tools before ResolveTranscript, then
  sends the resolved transcript to the converter. Codex normalizes once, uses
  that transcript for current tools, and shares one resolved transcript between
  instructions and input. The converter still records initial-system identity
  before TransformMessages filters failed history.
- Added no fields, public API, compatibility wrapper, dependencies or generalized
  preparation pipeline. Capability lookup and hook/auth ordering are unchanged.
  Updated private converter/plan test callers; no agent runtime source changes.
- New `TestResponsesRequestContextStages` covers 16 combinations of Responses/
  Codex, native/collapsed systems, no initial system/tools-only initial system,
  and error/aborted history, including declaration order and caller immutability.
  It passes on both this refactor and a read-only overlay of the original
  implementation with the prerequisite fix, proving existing wire behavior.
- Validation passed on Windows Go 1.26.4 with `GOWORK=off`:
  - `go test ./pkg/pigo -run 'TestResponses(ToolAnchors|SystemUpdatesSurvive|ToolAnchorPlan)' -count=1` before refactoring.
  - `go test ./pkg/pigo -run '^(TestResponsesToolAnchor.*|TestResponsesSystemUpdatesSurviveFailedHistoryFiltering|TestResponsesAnchoredFunctionStreamBoundaries|TestTranscriptOpenAIRequestNativeAndCollapsedSystems|TestToolSampling.*|TestConvertOpenAICodexMessages.*|TestCompleteSimpleOpenAICodex(BuildsRequestAndParsesText|NormalizesForeignToolCallIDsInRequest|PassesPreviousResponseID|OmitsEmptyPreviousResponseID)|TestResponsesCustomReplayAndFunctionFallback|TestOpenAIResponsesPreservesTerminalReasoningReplayFields)$' -count=1 -timeout=120s`.
  - `go build ./...`, `go vet ./pkg/pigo`, and `PIGO_LIVE_TEST=0 go test ./... -count=1 -timeout=120s`.
  - `go test ./pkg/pigo -run '^TestResponsesRequestContextStages$' -count=1`, also with `-overlay C:/Users/icatm/AppData/Local/Temp/pi-go-responses-context-stage1-original-overlay.json`.
  - `git diff --check`; independent read-only review found no actionable issues.
- No live provider, race run, separate examples-module verification or UI check
  this stage. No commit, push, tag, merge or downstream dependency update.
  P2 tool execution finalization remains outside this stage pending coordination.


## 2026-10-04: tool outcome preparation and validation (stage 2)

- Following coordination approval, preserved the reviewed stage-1 tree
  `bc5266daa8efcd9ad3e187f52033fc09d672e4d7` as two local commits:
  `3ab2c632e74856f9b62d574cbe41e576c94733c3` contains only the transplanted
  history-filtering defect fix and its regressions;
  `5a91d8a72f42449f1eab089a9829c43b92aca8b8` contains only the approved
  behavior-preserving Responses context refactor. The latter is the P2 parent.
  The prior worklog's uncommitted status describes the stage-1 review handoff.
- Kept the same isolated worktree and `codex/responses-context-stage1` branch.
  Original `V:/gitdownload/pi-go` stays on `03237e67c284f9d4d8ef55478e5a95f7524da7c5`;
  all three original dirty-file SHA-256 hashes remain unchanged.
- Extracted two private helpers in `agent/tool_lifecycle.go`:
  `toolOutcomeFromExecution` captures executor facts, error classification,
  bounded initial result and pre-hook JSON validation;
  `validateToolOutcome` checks the effective hook result, business failure,
  child output schema and host result validator. They use existing value types.
  This separates result policy from lifecycle coordination without a new state
  container, compatibility wrapper or generic framework.
- Kept permission checks, child scope ownership/close, update admission/wait,
  AfterToolCall, cancellation, state.finish, scope reports and final events in
  executePreparedTool, in their original order. Raw execution success remains
  independent of hook-adjusted success. Textual inlining of both helper bodies
  exactly reproduces the parent executePreparedTool body. Independent read-only
  review found no actionable semantic or aliasing issue; no performance claim.
- Added one behavior regression,
  `TestToolFinalizationRequiresRawAndEffectiveSuccessForState`, using RunToolCall,
  State.Stage and recordJournal: success accepts state; a hook clearing a raw
  business failure cannot accept state; a hook introducing business failure
  also rejects state. All three cases pass both before extraction (parent source
  via read-only Go overlay) and after extraction. No tests mirror the helpers.
- Validation passed on Windows Go 1.26.4 with `GOWORK=off`, `PIGO_LIVE_TEST=0`:
  - Parent baseline `go test ./agent -count=1 -timeout=120s`.
  - `go test ./agent -run '^(TestExecution(OutcomePreservesErrorPartialAndRemoteFacts|HookFailureRedactsRawOutputAndKeepsErrorChain)|TestGenericErrorPreservesTrustedRemoteFacts|TestExplicitResultValidatorRunsAfterHooksForDirectBusinessErrors|TestAfterHookBusinessFailureTracksEffectiveIsError|TestHostChildReportSurvivesParentHookFailure|TestLeafCannotSupplyForgedChildReport|TestExecutorPanic.*|TestAfterHookPanicCompletesChildAndKeepsExecutionFacts|TestResultValidatorPanicReturnsDirectFailureWithExecutionFacts|TestChild(AfterHookIsAuthoritativeAndOutputSchemaChecked|ScopeCloseRetainsActiveFactsAndSuppressesLateEvents|ErrorPartialResultRespectsConfiguredBudget|EndEventRespectsConfiguredSummaryBudget)|TestGeneratedChildFailureTextRespectsResultBudget|TestEngine(ProgressGateSerializesSettlement|IgnoresToolUpdatesAfterExecutionSettles|ToolResultDetailsAreIsolatedAcrossHooksEventsAndTranscript)|TestJournalStateLifetimeRejectionAndRevocation)$' -count=1`.
  - `go test ./agent/session -run '^TestRuntimeFinalizationRejectsStateAndRetainsChildFacts$' -count=1`.
  - `go test ./agent -run '^TestToolFinalizationRequiresRawAndEffectiveSuccessForState$' -count=1`, also against the parent overlay.
  - `go test ./... -count=1 -timeout=120s`, `go build ./...`, `go vet ./agent/...`, `git diff --check`.
- Local review delivery only. No push/tag/merge/release, downstream changes,
  new dependencies, live provider calls, race run, separate examples-module
  verification or UI checks. Stop at the coordination review point.


## 2026-10-04: bump package version to 0.13.1

- Bumped `pkg/pigo/version.go` and the examples module's pi-go requirement from
  0.13.0 to 0.13.1 for the reviewed history-filtering fix and behavior-preserving
  Responses/tool-outcome refactors now merged on master. The examples module's
  existing root-module replace is unchanged; no third-party dependency changed.
- Validation passed with `GOWORK=off`, `PIGO_LIVE_TEST=0`:
  `go test ./internal/cli -count=1 -timeout=90s`; examples module
  `go test ./... -count=1 -timeout=120s`; compiled `./cmd/pigo` and verified its
  `--help` banner is exactly `pigo 0.13.1`; `git diff --check`.
  Reused the previously verified implementation's full root test/build/vet
  evidence; no new live-provider or race validation for this metadata change.
- User authorized committing and pushing master. This change does not create
  a release tag or GitHub Release.

## 2026-10-05: SHA item 6, owned checkpoint approval bindings

- Base: `fad41fb810b54449abb4bc124ed21ecef933b7e5` (merged PR25–27).
  Live approval gates compare owned canonical definition/call identity, raw
  arguments and final parser/hook arguments directly. They no longer serialize
  and SHA-256 the binding envelope on each gate evaluation.
- Keep the public API and v1 checkpoint bytes unchanged. Compute the existing
  digest when persisting a new interrupt and validate it on Load, then carry
  private immutable bindings through decisions and save normalization. Already
  persisted partial decisions contain only the digest, so matching those uses
  the legacy digest fallback; guessing their final hook arguments would be unsafe.
- Retain PR27's post-custom-serialization pending-batch validation. The saved
  approval representation is also compared with its owned binding before that
  binding is reused. No equality or replay checks are removed.
- Regressions cover canonical identity and ownership, a fixed v1 digest vector,
  concurrent single-use owned/legacy approvals, and process restart after partial
  approval followed by hook-argument drift. Existing custom serializer and
  whole-batch reapproval tests remain passing.
- Validation: `go test ./agent/checkpoint`, `go test -race ./agent/checkpoint
  ./agent`, and `go test ./...` passed on Linux/amd64, Go 1.26.2. Independent
  review found no blocking issue and reran checkpoint race tests successfully.
  Live providers and the separate examples module were not run for this change.
- `BenchmarkApprovalGateAllow` was copied unchanged into a detached baseline
  worktree; both test binaries ran with `-test.cpu=1 -test.count=3` and allocations
  enabled on AMD EPYC 9V74. Median baseline → changed: 64-byte arguments,
  9,257 → 7,377 ns/op, 6,328 → 5,832 B/op, 79 → 76 allocs/op; 64-KiB arguments,
  2,590,445 → 1,900,529 ns/op, 1,813,938 → 1,748,130 B/op, 109 → 106 allocs/op.
  Shared-host timing is indicative; canonical argument serialization is retained.
- Implementation checkpoints and draft PR only; no merge or release.

## 2026-10-05: prepare v0.14.0 in the cloud

- Confirm the latest published tag/release is v0.13.0; the source-only 0.13.1
  bump was never tagged. Choose v0.14.0 for the public native-tool sampling and
  dynamic Responses declaration features since the last release, together with
  the reviewed correctness and performance changes through PR #30.
- Update pigo.Version and the examples dependency together; preserve all
  third-party dependency versions. Record capability restrictions, full-history
  requirements, and checkpoint v1 compatibility in the release notes.
- Reuse the implementation baseline's successful six-job CI run 37255985674
  for master 2105613fc327b900998db83df278ab98c5a0a5f1. The final release commit
  must pass the existing CI workflow before publication.
- Follow the existing release process entirely in the cloud: prepare five CLI
  archives and checksums from the clean tested master commit, then publish a new
  annotated tag and formal GitHub Release. Verify downloaded assets before
  closeout. Existing tags and releases must never be overwritten; no new release
  workflow, repository permission or account-access change is included.
- Cloud Go 1.26.2 checks passed for the version change: `go test
  -mod=readonly ./internal/cli -count=1`, and examples all-package tests and vet.
  A blocked proxy zip was retrieved from its official Git source; no dependency
  versions or checksum files changed. The cloud packaging helper receives
  independent review plus syntax and actual archive validation.

## 2026-10-08: Codex identity headers and thinking-specific sampling

- Follow only the two user-selected Pi v1.1.0 behaviors: Codex identity header
  precedence (`0cf65d2bf7cad7a58437a6c8a9172f29dbc43ea7`) and thinking-level
  sampling (`76dfb88f63ce51ff2e3fe2ead4fcf1f65f71f121`). Mistral is excluded.
- Reproduced the old SSE/WebSocket override failures with local HTTP servers.
  Both transports now apply provider defaults before model/caller originator
  and User-Agent headers. Credential-derived authorization/account headers and
  transport headers remain protected. Header merging is case-insensitive and
  does not mutate either input map.
- Added SamplingParams and SamplingParamsByThinkingLevel to the model/options
  surface. OpenAI Completions and Responses merge model defaults, effective pi
  thinking-level overrides, then request keys, over named request fields.
  Preserve explicit zero/null JSON values and isolate cloned maps. Other APIs
  do not apply these maps; no provider, catalog or dependency is added.
- Reproduced missing sampling payload fields and summary-only Responses
  requests lacking medium reasoning. Responses normalization now retains the
  logical pi level; wire translation happens at serialization/validation, so
  aliases and repeated normalization cannot select a different sampling tier.
  Consumers inspecting normalized Responses options now see logical levels,
  rather than mapped provider wire strings. README documents this distinction.
- Existing Responses capability/range validation checks the merged temperature
  and top_p, including request overrides of named fields, before network I/O.
  Tests cover direct/simple entry points, defaults and override precedence,
  disabled reasoning, clamping, wire aliases, summary-only medium, option reuse,
  invalid sampling, non-compatible APIs and cloned configuration isolation.
- Validation passed on Windows with GOWORK=off and PIGO_LIVE_TEST=0:
  root and examples `go test ./... -count=1 -timeout=120s`; focused pkg/pigo
  sampling/header/capability/clone regressions with `-race -count=1`; root
  `go vet ./...`; `git diff --check`. New behavior regressions failed before
  implementation and passed afterward. No live-provider or UI validation.
- Local changes only; no commit, push, tag, release or downstream changes.

## 2026-10-08: review repairs and authorized master push

- User authorized review, fixes and push. Fetched origin/master and integrated
  its 19 intervening commits by fast-forward to 619c07d. Preserved local changes
  in an identified stash during synchronization; the only conflict was the
  worklog, resolved by retaining both dated histories. Keep upstream v0.14.0
  metadata and all unrelated changes intact.
- Reproduced and fixed four review findings with local regressions:
  - A session socket could retain the previous originator/User-Agent after a
    caller override. Include effective identity headers in its cache key;
    equivalent header casing still reuses the connection.
  - NaN in an overridden named temperature/top_p failed base serialization
    before the valid sampling override was applied. Remove shadowed named
    values from the serialization copy, preserving explicit null overrides.
  - Whitespace around a Responses thinking level selected off sampling while
    the wire used high. Trim the logical sampling input like the wire mapper.
  - Sampling maps could replace model/reasoning/tools/stream after capability
    and tool-plan validation. Reject protocol-control fields during request
    serialization, before network I/O, instead of silently dropping them.
    This is an intentional restriction relative to upstream's unrestricted
    Object.assign; provider-specific sampling keys remain supported.
- All four regressions failed before their fixes and now pass. Final Windows
  validation on the synchronized tree passed with GOWORK=off, PIGO_LIVE_TEST=0:
  root and examples `go test ./... -count=1 -timeout=120s`; full provider
  `go test -race ./pkg/pigo -count=1 -timeout=120s`; root/examples `go vet ./...`;
  root `go build ./...`; `git diff --check`. No live-provider calls or UI checks.
- Commit and push only the reviewed header/sampling change and its tests/docs
  to master. No tag, new release, dependency upgrade or Mistral change.

## 2026-10-14: Command Code current model catalog and protocol support

- Refresh the deterministic Command Code catalog to the 89 chat models returned
  by `https://api.commandcode.ai/provider/v1/models` on 2026-10-14. Add the current
  Claude 5.5, GPT-6/6.1, DeepSeek V4.1, MiMo V2.6, Qwen Omni, GLM FlashX,
  Step 5, Mistral Large 4 and free-model entries, with input capabilities,
  selectable reasoning efforts, output limits and estimated token prices.
- Sources: `pi-commandcode-provider` v0.7.7 (published 2026-10-11; generated
  capability data from `command-code` 1.79.2 and pricing verified 2026-10-09),
  plus Gemini 4 Argon and Muse Wonder from official `command-code` 1.82.1
  (published 2026-10-13) and their Command Code model pricing pages. Downloaded
  npm archives were integrity-checked. No new runtime dependency was added.
  Sources remain active; the native Go implementation uses the existing
  Go 1.26 module and HTTP/protocol adapters on Windows.
- Read `supported_endpoints`, prefer Messages/Responses/Chat Completions in that
  order, and exclude non-chat endpoints such as System One. Preserve the chosen
  protocol through model cloning, registry refresh and the Pi-compatible v1
  cache's `api` field. Older caches/manual models without endpoint metadata keep
  the established name-based selection. Invalid explicit protocols fail before
  HTTP; only the existing `403 upgrade_required` response selects generate.
- Keep protocol adaptation in `pkg/pigo`. Command Code Responses uses system
  prompts, omits optional reasoning controls for off, and consumes its
  `response.reasoning.delta` alias without rewriting raw provider observations.
  The shared Responses adapter gains an optional developer-role capability;
  other providers retain their previous default. Agent changes are tests only.
- Include the current context-price tiers. DeepSeek cost estimates deliberately
  remain documented off-peak snapshots, not authoritative live billing prices.
- Regression tests first reproduced missing built-in models, ignored endpoints,
  dropped incremental reasoning, developer-role prompts and off effort on the
  Responses path. Cover the fixes plus image prompts, two-turn tool replay,
  cached-token usage, tier boundaries, cache protocol retention, non-chat
  exclusion, malformed metadata and cloned protocol isolation.
- Validation passed with GOWORK=off and PIGO_LIVE_TEST=0: focused Command Code
  tests in pkg/pigo, agent and internal/cli; root and examples `go test ./...
  -count=1 -timeout=120s`; root `go vet ./...`; focused Command Code/clone/Responses
  compatibility tests with `-race` in pkg/pigo and agent; `git diff --check`.
  Built a temporary Windows CLI and verified public model discovery and an
  isolated cache: 89 models, 11 Messages / 70 Responses / 8 Chat Completions.
- Initial implementation did not include a commit or push. No live inference,
  UI acceptance, tag, release or downstream repository change. Public catalog
  discovery is not live model-generation proof.

### Review and authorized commit/push

- Review reproduced two defects with failing regression tests before fixing
  them. Responses sent unsupported `effort: off` for whitespace-padded off,
  models without reasoning or selectable efforts, and summary-only requests
  whose implicit medium level is unsupported. Resolve summary defaults and
  check the model's effective level before clearing optional reasoning controls.
- The Command Code reasoning-delta alias accepted null, string, negative and
  fractional output indexes as valid thinking content. Validate the advertised
  non-negative integer field before emitting content; preserve other providers'
  event handling. Strengthen the two-turn test to assert the image payload.
- Final validation passed with GOWORK=off and PIGO_LIVE_TEST=0: focused Command
  Code tests; root and examples `go test ./... -count=1 -timeout=120s`; full
  `go test -race ./pkg/pigo ./agent -count=1 -timeout=120s`; root `go vet ./...`
  and `go build ./...`; `git diff --check`. No remaining review findings.
- The user authorized committing and pushing this reviewed change. Fetched
  origin and confirmed master matched the local baseline before committing.
  Scope remains Command Code models/protocols, related tests and documentation;
  no release or live inference was performed.

## 2026-10-14: Command Code live model capability verification

- User requested real inference checks for reasoning, tool calls and image
  input. Built the package test executable with GOWORK=off, then ran only the
  explicitly gated `TestCommandCodeCapabilitiesLive` with selected model IDs.
  Requests use synthetic prompts, a locally generated four-color PNG, and a
  random nonce returned by an in-memory tool. No user documents or credentials
  appear in logged evidence. Each call caps output at 4096 tokens, times out at
  90 seconds, and disables automatic retries.
- Current runtime credentials are identified by the service as the Go plan.
  `/provider/v1/responses` returns 403 `permission_error/upgrade_required`, so
  the existing adapter selects `/alpha/generate`. Messages returns 403
  `permission_error` without the upgrade code and is not broadened into a
  generic permission-error fallback. Public catalog discovery succeeds, but
  no native Messages/Responses inference succeeded with these credentials.
- DeepSeek V4.1 Flash (`deepseek/deepseek-v4.1-flash`, high): all three checks
  passed through generate. Arithmetic returned 423 with 88 nonempty thinking
  deltas; one tool call had the expected name/arguments and consumed the random
  result on the next turn; PNG quadrants returned red,blue,green,yellow.
- Qwen 3.8 Omni Flash (`Qwen/Qwen3.8-Omni-Flash`, xhigh): final probe passed all
  three checks, with 91 nonempty thinking deltas for the arithmetic request and
  correct tool replay/image answer. The first probe requested unsupported high,
  which the generate adapter omitted; it produced a correct numeric answer with
  unwanted prose. Its independent image request also emitted a literal
  `</think>` and duplicate answer. Select a supported effort in the harness and
  retain these observed output-format anomalies; a later pass does not prove
  output stability.
- GPT-6 Luna (`gpt-6-luna`, high): tool call/result replay passed twice. No raw
  reasoning-delta events arrived in either reasoning attempt (one had only
  reasoning-start/end). Arithmetic returned 278 first and 423 on diagnostic
  reproduction. Image recognition twice returned yellow,yellow,green,green
  instead of red,blue,green,yellow. The diagnostic payload checks confirmed the
  requested model/effort and unmodified image bytes. This is a failed live
  validation; no parser filtering or retry was added to hide the result.
- Claude Haiku 5.5 and Gemini 4 Argon remain unverified: the generate endpoint
  explicitly rejects them with MODEL_NOT_IN_PLAN, requiring GOAT+ and Pro+
  respectively. No subscription or billing settings were changed. Official
  https://commandcode.ai/docs/plans/goat#api-support confirms Go lacks Provider
  API access and GOAT includes it; eligible credentials are needed to complete
  native protocol checks.
- Added `pkg/pigo/provider_commandcode_capabilities_live_test.go` with the
  additional PIGO_COMMANDCODE_LIVE_MODELS opt-in, live-catalog lookup, bounded
  requests, protocol/error-shape evidence, input-byte assertions and strict
  output checks. Offline focused Command Code tests, pkg/pigo vet and diff
  whitespace checks pass with PIGO_LIVE_TEST=0. No production code changed;
  this test and work-log update remain local, without a new commit or push.

### GOAT credential follow-up and Luna image diagnosis

- The user supplied `CommadCode_GOAT` in `V:\env\.env`. Read only that entry
  into the test process's canonical COMMAND_CODE_API_KEY environment variable;
  never printed or copied its value into repository files. The new credential
  succeeds directly on `/provider/v1/messages` and `/provider/v1/responses`,
  without the Go-plan generate fallback.
- Claude Haiku 5.5: arithmetic, tool call/result replay and PNG input pass on
  Messages. Adaptive/high reasoning returns a thinking block and an empty
  thinking delta plus a nonempty signature (1604/1692 characters in diagnostic
  samples); raw thinking text length is zero. The parsed ThinkingContent keeps
  that signature. This is opaque signed reasoning, not a missing parser delta.
  The live check now distinguishes signed reasoning from visible thinking and
  does not claim the former exposes a reasoning stream.
- DeepSeek V4.1 Flash: all three checks pass on native Responses. Arithmetic
  returns 423 with 72 nonempty thinking deltas; tool replay and the same PNG
  fixture succeed. This completes native-protocol evidence previously blocked
  by the Go credential.
- GPT-6 Luna: native Responses arithmetic returns 423 with 105 nonempty
  reasoning-summary deltas; tool call and result replay pass. The earlier
  missing-delta finding was observed on the Go/generate path, whose raw stream
  did not contain text deltas for reasoning. It is not a general inability of
  Luna or pi-go to stream reasoning through the native API.
- Luna PNG recognition still fails with the new key. The same synthetic image
  has red/blue/green/yellow quadrants; pi-go sends unchanged image bytes, yet
  native Responses returns yellow/yellow/green/green. Bypassed pi-go entirely
  using bounded standard-library HTTP requests: Responses returned
  yellow/yellow/green/yellow and Chat Completions yellow/yellow/green/cyan for
  the PNG. A separately encoded RGB PNG with no scanline filtering/compression
  also failed (yellow/yellow/green/red), ruling out a Go PNG filter/compression
  detail as the explanation. Each fixture was decoded locally and its quadrant
  center pixels checked before transmission.
- The same image encoded as JPEG (quality 95) returns red/blue/green/yellow in
  two direct HTTP probes and the pi-go Responses probe. DeepSeek recognizes
  the original PNG using the same direct Responses request. Evidence therefore
  localizes the remaining fault to the upstream Command Code/Luna PNG handling
  or recognition path; it does not identify which upstream internal layer is
  responsible. No automatic image conversion or production fallback was added.
  Representative Luna request IDs: PNG Responses gen_01M4WJRVFCYX55HHCV6MVHTBQR;
  PNG Chat gen_01M4WJRZCD70JND9RD5MSCQ34F; unfiltered PNG
  gen_01M4WJVM2M8FWXS6Z9F3FV214G; JPEG through pi-go
  gen_01M4WJZBYR27D19FV4F04KGY45.
- The gated test supports PIGO_COMMANDCODE_LIVE_IMAGE_FORMAT=png|jpeg (default
  png), logs only event types, field sizes and safe protocol parameters, and
  preserves strict image assertions. Final live Haiku/DeepSeek suite and Luna
  JPEG probe pass; Luna PNG remains a documented failure. Focused offline
  Command Code tests, pkg/pigo vet and diff whitespace checks pass. Production
  code, persisted environment settings and the prior pushed commit are
  unchanged; test/work-log changes remain local.

### Model adaptation fixes after GOAT live validation

- User approved automatic PNG-to-JPEG preparation for Command Code GPT-6 Luna.
  The adapter now converts user and tool-result PNGs before protocol selection,
  using Go's standard library, JPEG quality 95, unchanged dimensions and a white
  background for transparency. Caller history stays unchanged; already prepared
  JPEGs are not re-encoded. Other models and formats retain their original data.
  Invalid PNG/base64 and excessive dimensions fail before HTTP. Local decode
  limits are 32 MiB PNG data, 64 Mi pixels and JPEG's 65535-pixel side limit.
- Fixed the shared Anthropic serializer dropping signed thinking with empty
  visible text. Same-model/provider/protocol replay now retains the exact empty
  thinking field and signature; foreign opaque signatures and unsigned empty
  blocks remain excluded. Two-turn Command Code regression coverage reproduces
  the original dropped block and verifies the corrected wire payload.
- Generate requests now trim and clamp requested reasoning levels against model
  metadata, matching native routing: Qwen 3.8 high selects xhigh and minimal
  selects low; off, non-reasoning and non-selectable models omit the effort.
- Regression tests failed before the fixes and pass afterward. Added image
  tests for alpha compositing, unchanged dimensions, caller immutability,
  idempotence, unaffected models, malformed/oversized headers, and actual
  Responses/Completions/upgrade-to-generate payloads. Replaced the older Luna
  fake-PNG fixture with a real PNG. README documents conversion and limits.
- GOAT live validation after production changes: Haiku 5.5 and Qwen 3.8 Omni
  Flash pass reasoning, tool call/result replay and image checks. Luna passes
  tools and the original PNG fixture through automatic JPEG preparation
  (gen_01M4WKXMDTXBTRE5XP9K2EX4WT). No retry or protocol fallback was needed.
- The first Luna reasoning check returned the correct answer plus encrypted
  reasoning, with no visible deltas. The harness previously only recognized
  opaque Anthropic signatures; it now requires a real Responses encrypted
  payload when visible deltas are absent, and checks that payload on replay.
  A follow-up Luna run returned 108 visible reasoning deltas and successfully
  replayed its encrypted block (gen_01M4WM1KTCP109X2FCXXH2V54S followed by
  gen_01M4WM1RKJPNZEEK0WESKGHETQ).
- Haiku's opaque signature replay is verified on real Messages requests,
  including exact signature presence in the outbound payload. One arithmetic
  answer was Markdown-bold 423; the semantic arithmetic check now permits a
  single bold/code wrapper while rejecting extra prose or wrong numbers.
  The final focused run passes both reasoning and continuation, with a
  1700-character opaque signature and no visible thinking text
  (msg_011Cg1iTazVPmvfbKatZjAgD -> msg_011Cg1iTnss5h3mfzyvvJix9).
- Validation: root and examples go test ./... pass; root go vet ./... and
  go build ./... pass; focused Command Code/Anthropic/Kimi/Transform race tests
  pass. Final focused regression tests pass. Live checks use a compiled test
  executable, explicit model selection, MaxRetries=0 and the temporary process
  credential only. No environment file, stored authentication or dependencies
  were changed. No UI testing or release/tag is part of this update.

## 2026-10-14: Publish v0.15.0

- User requested a new pi-go release containing the Command Code catalog and
  live-validation fixes. Latest published version is v0.14.0; choose v0.15.0
  for the new model/protocol support and public sampling configuration surface.
  Include master changes 8d958d9, fe8f5d5 and 48c9a4f, update pigo.Version and
  the examples requirement together, and retain all third-party versions.
- Release notes describe native endpoint selection, bounded upgrade-required
  fallback, opaque reasoning replay, supported effort clamping and Luna PNG
  preparation. Call out lossy JPEG/white compositing and local decoding limits,
  plan-dependent access, logical Responses thinking levels and Go 1.26.2.
- Follow the existing publication process: verify the final release commit's
  six CI jobs, build five CGO-disabled CLI targets from a clean checkout, check
  embedded VCS revision/clean flags and Unix executable modes, and publish a
  new annotated tag plus formal GitHub Release with SHA256SUMS.txt. Download
  and verify every asset and check public Go module resolution. Keep generated
  packaging and publication evidence under ignored dist/release-v0.15.0.
- Reuse the completed implementation tests and paid synthetic model probes
  recorded above. Validate the version bump locally and rely on exact-commit
  CI for the release-wide platform checks. No additional live-model calls,
  downstream repository edits, UI acceptance or new release workflow.
- Exact-release CI 38527695410 exposed an existing Windows OAuth lock race in
  TestOAuthFileStoreCrossProcessCAS: Mkdir returned ERROR_ACCESS_DENIED while
  another process's directory deletion was pending. A normal local stress run
  did not reproduce it, so added a deterministic Windows fixture that marks
  the lock for deletion while keeping its handle open. The regression fails
  before the fix with the same public storage failure.
- Preserve the exclusive directory-lock protocol. Windows creation now uses
  NtCreateFile(FILE_CREATE), distinguishing STATUS_DELETE_PENDING and existing
  name collisions from permission errors before Win32 collapses the status.
  Close the creation handle immediately; keep the existing contention deadline,
  cancellation and stale-lock handling. Other operating systems still use
  Mkdir. Reuse the existing x/sys version; no library or lock-format migration.
  Windows tests cover pending deletion, acquisition after the final handle
  closes, real ACL denial without retry, and extended-length paths. The native
  status and directory lifetime follow Microsoft NtCreateFile/RemoveDirectory
  documentation. Replace only the unpublished local release tag after the fix;
  rebuild all artifacts and require the new exact commit's full CI to pass.
- The Windows deletion-pending regression passes after the fix. All OAuth
  file-store regressions pass for 12 race-enabled iterations using Go 1.26.2
  (the CI toolchain); root all-package tests and vet pass with Go 1.26.4.
  Reference: https://learn.microsoft.com/en-us/windows/win32/api/winternl/nf-winternl-ntcreatefile

## 2026-10-15 - Preserve provider failure origin for relay diagnostics

- Repair the error-origin loss at the SDK boundary. ProviderFailureOf reads a
  structured provider_failure diagnostic from AssistantMessage; the diagnostic
  survives cloning, streamed terminal errors and Context JSON persistence.
  Native Chat Completions, Responses and Anthropic Messages identify explicit
  upstream HTTP/error events, interrupted response reads/missing terminal
  events, and response decoding failures. Local callbacks, cancellation and
  unclassified SDK failures remain distinct from upstream failures.
- Preserve original SDK error text and add interrupted HTTP error-body evidence.
  Error wrappers keep errors.Is/As identity. Retry predicates, counters, delays,
  output/usage guards and configured budgets are unchanged; diagnostics are
  evidence only and never grant retry permission.
- P1 regression extends protocol failure coverage because prior stream/result
  tests did not assert error origin and persistence together. Cover the reported
  HTTP-200 ambiguity, malformed JSON/field types/finish reasons, EOF, DONE without
  a terminal event, reader failures, non-2xx and interrupted error bodies across
  the three native protocols. Existing retry and HTTP evidence tests also pass.
- Verified: focused pkg/pigo ProviderFailure, ProviderHTTP, HTTPStreamClient,
  OpenAI Responses/Completions, Anthropic, stream retry and callback contracts
  (2.759s), plus go vet ./pkg/pigo. No whole-repository tests/race or paid live
  model requests. No dependency or source-version changes.
- User authorizes pushing this repair to an independent source branch so
  ai-relay can pin a downloadable immutable commit. Formal release/tag and
  production deployment are outside this step.
- Final source audit found that the Anthropic hosted continuation boundary
  returned an untyped missing-terminal error after recording its typed version,
  so the outer error handler overwrote the diagnostic. Extend the existing
  hosted missing-message-stop regression: it fails with sdk_error/unclassified
  before the repair and passes after propagating the same typed error. Focused
  diagnostic/hosted/Anthropic/retry tests (0.359s) and pkg/pigo vet pass.
- Pre-push consumer review reproduced a false response_parse_error for local
  request serialization: an invalid RawMessage returned by OnPayload raises a
  JSON syntax error before any HTTP request. Remove JSON-type guessing from
  the generic error recorder; response readers retain explicit classification
  at the decoding boundary. Extend the existing P1 diagnostics test to assert
  sdk_error/unclassified, retained error text and zero dispatches. The new case
  fails before the fix; focused protocol/HTTP/hosted/retry/callback tests
  (2.627s) and pkg/pigo vet pass afterwards. Retry behavior remains unchanged.

## 2026-10-23 — Explicit trusted skill catalogs

- Add opt-in `agent/skills`, inspired by Pi coding-agent's metadata catalog and
  on-demand SKILL.md loading. Keep skill discovery/read lifecycle out of the
  single-agent loop and document that skills are guidance, not permissions.
- Require explicit trusted roots, strict typed frontmatter, deterministic
  first-valid-wins diagnostics, bounded text/discovery and confined resource
  reads through pinned Go `os.Root` handles. Reload atomically publishes the next
  index; fatal errors preserve the previous snapshot. No HOME scan, network,
  execution, dependency installation, tenant policy or document business layer.
- Add only yaml.v3 v3.0.1; reuse existing cloud cache for offline validation.
- Focused package race tests and vet pass with Go 1.26.5 on Linux. Coverage includes
  malformed metadata, catalog escaping, path and symlink confinement, duplicate
  selection, limits, metadata changes, retained handles and concurrent reload.
  No live models, CI, release/tag, or deployment performed. Windows/macOS runtime
  behavior and downstream JZ integration remain unverified.
- Existing `agent` tests also pass; Windows amd64 skill tests cross-compile.
  Broader `agent/toolset` verification could not start with the offline cache
  (missing wazero v1.12.0, x/oauth2 v0.35.0, x/sync v0.20.0); no claim of a
  complete repository regression pass is made.

### Compile-time FS skill snapshots

- Add `NewFS` / immutable `FSCatalog` for trusted `embed.FS` skill bundles, sharing
  the strict frontmatter parser and prompt formatter with the disk catalog.
  Construction copies bounded text; subsequent reads never access the source.
  Relative FS roots, text/entry/depth/aggregate limits, first-name diagnostics,
  symlink/special-resource rejection and detached metadata are covered by tests.
- Expose an application-assigned version label, not a digest or permission claim.
  No runtime reload, remote install, execution, tenant logic, or real deployment.
- MapFS and actual go:embed fixtures cover immutable reads after source changes,
  disk/FS semantic parity, explicit-only skills, path rejection, all size limits,
  invalid text, symlinks, bundle boundaries and concurrent reads. Linux race tests
  and vet pass with the existing offline Go 1.26.5 toolchain.

## 2026-10-23 - Integrate provider diagnostics for downstream upgrades

- The latest master declares version 0.16.0, but its published module omitted
  the three provider-diagnostic fixes already consumed by ai-relay. A temporary
  consumer modfile reproduced undefined ProviderFailureOf and failure-kind
  constants. The user explicitly authorizes integrating and pushing these
  existing fixes before updating ai-relay to a downloadable module revision.
- Merge the provider-failure-diagnostics branch into current master. Resolve
  the worklog-only conflict by retaining both dated entries. Provider production
  code matches the previously consumed diagnostics branch; the new skill
  catalog APIs and version declaration remain intact.
- Integration tests exposed fixture assumptions. Stream/complete comparisons
  now ignore per-call diagnostic timestamps on a copied slice while retaining
  every diagnostic field. Embedded skill resources compare with the actual
  embedded bytes, including Windows CRLF. Reload failure uses a missing empty
  root on all platforms; Unix still checks reads across rename, and Windows
  checks rename after Close to prove successful and failed reloads release
  their handles. The two skills failures reproduce on unmodified master.
- Windows Go 1.26.5, GOWORK=off: focused diagnostics, hosted continuation and
  fixture regressions pass; full pkg/pigo and agent/skills race tests pass;
  package vet, module verification and tidy-diff pass. Tidy adds missing sums
  for yaml.v3's existing test dependency without changing module versions.
- Publication is an ordinary master fast-forward containing both histories.
  No existing tag is moved and no release, binary packaging, paid provider
  call, deployment or UI acceptance is performed. Full repository/agent/runtime
  suites and non-Windows execution are outside this integration check.

## 2026-10-23 - Port independent JZBeacon agent improvements

- Add host-owned ToolDefinition.MaxArgumentBytes. Check raw JSON before parsing,
  parsed and schema-normalized JSON, and before-hook mutations before approval
  or execution. Direct/model/child calls share the existing lifecycle. Child
  resource bounds remain independent; failure code/reason is resource/argument_bytes
  with not-started/not-dispatched facts and parser/hook error causes preserved.
- Add agent/strictschema as an opt-in closed-shape declaration converter and
  optional-null omission normalizer. Freeze schemas, preserve scalar constraints,
  empty values and numeric lexemes, enforce input/output bounds, and reject
  ambiguous or unsupported shapes. It does not enable provider strict mode or
  replace the ordinary schema/business validator. No dependency is added.
- Extend session.CompactionOptions with ProtectedTailMessages. The existing
  complete-turn planner retains the protected suffix and tool pairs, alongside
  token retention and current system declarations, without rewriting a plan after
  selection or mutating the durable branch.
- Keep JZ-specific context epochs/CAS, tenant/source authorization, task plans,
  memory-space ownership, result receipt deduplication, endpoint network policy,
  relay-specific token estimation and published route output budgets in the host.
  Complete-turn compaction and MCP catalog validation already exist in pi-go.
- Windows Go 1.26.4, GOWORK=off: all agent/... tests passed; agent, agent/session
  and agent/strictschema race tests passed; agent/... vet and diff checks passed.
  Tests use local fakes only. Provider/live/full-repository suites were not run.
- Work is isolated from the original pi-go checkout's existing edits. JZBeacon's
  dependency pin and application code are unchanged; no local replace, release
  tag, production operation or new SHA-based scheme is introduced.

## 2026-10-23 - Fix PR 33 omission and preparation-error regressions

- Reproduce both review findings with new failing regressions before changing
  production code. The default anyOf validator deliberately coerces values in
  branch order, so retain that existing runtime contract and fix the generated
  nullable declaration: match exact null first, guarded by enum=[null]. This
  keeps omitted scalars null and prevents empty strings, zero or false from
  entering the null branch after failing their original constraints.
- Cover raw and parsed default tool arguments, nested objects/arrays, empty
  containers, required nulls and valid scalar zeros. Verify minLength, pattern,
  numeric minimum, enum and const failures stop before approval or execution;
  valid values and optional null omission still work.
- Preserve parser and before-hook errors inside Unicode rejections by joining
  their causes under the existing typed error. Keep the invalid_unicode reason,
  not-started/not-dispatched facts and safe presentation. Direct and child-call
  regressions verify both errors.Is chains and that host error text stays out
  of the displayed error.
- Windows Go 1.26.4, GOWORK=off: focused regressions pass after the fix;
  go test -mod=readonly ./agent/... -count=1, race tests for agent, agent/session
  and agent/strictschema, go vet -mod=readonly ./agent/... and git diff --check
  pass. Provider/live, full-repository and non-Windows tests were not run.
- Update the existing draft PR branch using a normal push and retain its
  [skip ci] publication convention. No dependency, version or tag changes.

## 2026-10-23 - Resolve the five skills review findings

- Reproduce the UTF-8 BOM failure in both disk and FS catalogs before changing
  production code. Strip exactly one leading BOM before checking frontmatter;
  preserve BOM characters in the body and preserve all resource bytes. Expand
  disk/FS parity tests across LF, CRLF, BOM+LF and BOM+CRLF, including raw
  SKILL.md reads. Cover BOM with an empty body, misplaced/repeated BOMs and YAML
  merge-key rejection without weakening the existing strict metadata checks.
- Replace archived gopkg.in/yaml.v3 with go.yaml.in/yaml/v3 v3.0.5 from the
  [YAML organization](https://github.com/yaml/go-yaml). Maintenance checked on
  2026-10-23: the selected stable version was released on 2026-07-26, the v3
  branch was updated on 2026-10-13, and the repository is active. Its Go 1.16
  minimum and pure-Go implementation fit this module; the Node API is unchanged.
  v3 is the maintained stable/security-fix line; v4 remains a release candidate.
  The alternative goccy/go-yaml had no repository update within six months,
  so it was not selected. Open-issue searches found no Windows/Go 1.26 matches;
  local compatibility is established by the Windows checks below, not that search.
- Retain and verify the already-integrated Windows reload/close regression and
  embedded-resource byte comparison. Failed reload keeps the previous snapshot,
  Close releases Windows handles, and embedded CRLF resources remain unchanged.
- Tidy removes the old YAML and check.v1 entries from the root module. Root and
  examples resolve their complete dependency graphs with -mod=readonly. The
  examples module retains its existing indirect YAML dependencies and versions.
  Add these graph checks to CI and include agent/skills in the existing five-OS
  race matrix. Only standard Go dependency checksum entries change; no custom
  content hashes or version/tag changes are introduced.
- Windows Go 1.26.4, GOWORK=off, live-model tests disabled: agent/skills race
  tests pass; root and examples both pass full go test -mod=readonly -count=1
  ./..., go vet -mod=readonly ./..., go list -mod=readonly -m all, go mod verify
  and go mod tidy -diff. The examples tidy comparison required local LF
  normalization of checkout-only CRLF, with no dependency/content diff.
  git diff --check passes. Linux/macOS execution and the CI matrix were not run.

## 2026-10-23 - Review latest PR 33 after repository synchronization

- Fetch origin and confirm local master already matches origin/master. Preserve
  all eight pre-existing modified paths. Review the latest open draft PR 33 in
  its existing clean checkout; do not modify its production code or PR state.
- Reproduce a strictschema integration defect: the default agent validator
  coerces optional null scalars into empty strings, zero or false before
  Normalize can remove them. With an optional minLength=1 string, an explicit
  empty string is instead coerced to null and omitted, allowing execution that
  the original schema rejects. The regression test uses a custom raw parser
  and therefore misses the ordinary parser path.
- Reproduce lost error identity when a before hook both mutates an argument to
  invalid Unicode and returns a sentinel error. MaxArgumentBytes=0 preserves
  errors.Is; enabling the new limit drops the hook cause in the Unicode check.
  The corresponding post-parser Unicode branch also replaces the original error.
- Windows Go 1.26.4, GOWORK=off: agent/... tests and vet pass, as do race tests
  for agent, agent/session and agent/strictschema and the PR diff whitespace
  check. Standalone local executables reproduce both findings without changing
  the reviewed checkout. The PR has no remote CI results; live providers,
  non-Windows execution and full-repository tests were not run in this review.

## 2026-10-23 - Re-review and merge PR 33

- Re-review the complete latest PR diff, the exact-null omission declaration,
  parser/hook error chains, argument limits and protected compaction boundary.
  No remaining blocking findings. Confirm the reviewed head is unchanged from
  the previously passing three-package race run and includes current master.
- On Windows Go 1.26.4 with GOWORK=off, focused strictschema/argument-budget/
  protected-compaction regressions pass. Both root and examples modules also
  pass go test -mod=readonly ./... -count=1 and go vet -mod=readonly ./... with
  live-provider tests explicitly disabled. PR diff whitespace checks pass.
- Update the PR validation evidence, mark the draft ready and merge through
  GitHub with an expected-head guard under the user's authorization. Confirm
  GitHub reports merged, remote master contains the reviewed head and the merge
  tree exactly equals the tested tree. No source changes were needed this pass.
- Pre-merge remote CI had no results. Non-Windows execution, live providers,
  post-merge remote CI and a full-repository race suite are not verified here.
  Preserve the primary checkout's existing eight modified paths; its local
  master remains at its prior commit rather than rewriting uncommitted work.

## 2026-10-23 - Synchronize local master while preserving local edits

- Fetch origin and fast-forward local master to the merged PR 33. Only the
  shared worklog overlaps the eight existing modified files, so temporarily
  stash that path and keep the other seven files in place.
- Resolve the worklog append conflict by retaining all incoming and local
  entries. Restore the original unstaged state and verify the other seven
  files byte-for-byte against the pre-sync snapshot.
- Confirm local master matches origin/master, the index has no staged or
  unresolved changes and git diff --check passes. Keep the existing stashes;
  remove only this synchronization's temporary stash after verification.
  This operation does not commit or push the preserved local work.

## 2026-10-23 - Prepare minor version 0.17.0

- Advance pkg/pigo.Version from 0.16.0 to 0.17.0 for the additive agent and
  provider APIs merged since the previous version declaration. Add release
  preparation notes covering argument bounds, strict-schema omission, protected
  compaction tails and classified provider failures.
- Retain the examples module's existing published-version requirement and its
  local replacement, following the previous version-preparation convention.
  Preserve all pre-existing uncommitted files and earlier worklog entries.
- Windows Go 1.26.4, GOWORK=off: go test -mod=readonly ./internal/cli -count=1
  passes; compile cmd/pigo and verify pigo --help reports pigo 0.17.0. Full
  repository and live-provider suites are not rerun for this metadata update.
- This request updates local version metadata and notes only; no commit, push,
  Git tag, GitHub Release or binary publication is performed.

## 2026-10-23 - Prepare v0.17.0 source tag publication

- The user authorized committing and pushing the version update and creating
  the annotated v0.17.0 tag. Scope the commit to the version constant, release
  notes, examples module version and these version worklog entries. Preserve
  the earlier local skills, dependency, CI and worklog edits outside this commit.
- Advance the examples requirement to github.com/Icatme/pi-go v0.17.0 while
  retaining the existing local replacement. Refresh the release notes for
  source-tag publication; no GitHub Release or binary assets are included.
- Validate an isolated snapshot of the committed baseline plus the two version
  declarations, excluding unrelated local changes. Windows Go 1.26.4,
  GOWORK=off, live-provider tests disabled: root and examples both pass
  go test -mod=readonly ./... -count=1 and go vet -mod=readonly ./...;
  examples resolves the expected local v0.17.0 module. The compiled CLI
  reports pigo 0.17.0. Non-Windows execution and remote CI are not verified.
- Publication uses one atomic push for master and the annotated tag, followed
  by remote-ref verification. Normal Git commit/tag identities are checked
  for this publication; no additional checksum scheme is introduced.

## 2026-10-23 - Review and publish the remaining skills changes

- Review all eight remaining modified paths before the authorized commit/push.
  No blocking findings. The parser removes exactly one initial UTF-8 BOM before
  checking the frontmatter delimiter; body BOMs and raw resource text remain
  intact. Disk/FS parity covers LF, CRLF, optional leading BOM and raw reads;
  malformed metadata, duplicate BOMs, aliases and merge keys remain rejected.
- Recheck the YAML dependency against upstream sources: the original repository
  is archived; the YAML organization fork is active, with recent main-branch
  work on 2026-10-22 and v3.0.5 released on 2026-07-26. Its Go 1.16 minimum
  fits this module. v3 receives security fixes only; the current v4 version is
  still v4.0.0-rc.6, so keep the stable v3 API for this focused change. The
  alternative goccy/go-yaml's latest commit is dated 2026-04-07, over six months
  ago. Upstream v3 CI is Linux-based; Windows compatibility is checked locally.
  Sources: https://github.com/yaml/go-yaml/blob/main/README.md and
  https://github.com/yaml/go-yaml/releases/tag/v3.0.5.
- Windows Go 1.26.4, GOWORK=off, live-provider tests disabled: skills race tests
  pass. Root and examples both pass go test -mod=readonly ./... -count=1,
  go vet -mod=readonly ./..., go list -mod=readonly -m all, go mod verify and
  go mod tidy -diff. Changed Go files are gofmt-clean and diff checks pass.
  No production code or regression-test repairs were needed during this review.
- Include the earlier preserved worklog entries in the authorized eight-file
  commit. Publish to master without moving the existing v0.17.0 source tag.
  Non-Windows execution and post-push remote CI are not verified here.

## 2026-10-23 - Reorganize the README and documentation navigation

- Reduce the root README from 437 to 160 lines, with installation, a complete
  quick start, CLI usage, package responsibilities and runnable example links.
  Replace the overlapping highlights/positioning sections with a short overview.
- Move provider/authentication details, account quotas, model streaming/sampling
  and development commands into four focused guides under docs/. Rebuild the
  documentation index around guides, agent references, MCP and design notes.
  Preserve the existing provider, quota and sampling contracts in their guides.
- The quick start now reads an environment credential, bounds the request with
  a context deadline, checks its stop reason and prints the text response.
  The streaming guide handles events, final results and cancellation explicitly.
- Documentation validation: 77 local links/anchors resolve; Markdown fences and
  whitespace checks pass; all 14 PowerShell blocks parse. Four Go snippets
  (quick start, streaming, sampling and quota) compile with GOWORK=off and
  -mod=readonly. These snippets are not executed against live providers.
  git diff --check passes. Full test suites are not rerun for this docs-only change.

## 2026-10-23 - MIT licensing and v0.17.1 release preparation

- Add the standard MIT license for Icatme and pi-go contributors, preserving
  Mario Zechner's original Pi AI copyright notice. Add the README license
  badge and section; retain the existing QuickJS upstream notices.
- Include the preceding README/documentation reorganization in this release.
  Advance pkg/pigo.Version and the examples module requirement to 0.17.1;
  keep the published v0.17.0 tag intact. Add release notes for the 0.17 series.
- Windows Go 1.26.4, GOWORK=off, all live gates disabled: root and examples
  pass go test -mod=readonly -race ./... -count=1, go vet -mod=readonly ./...
  and go mod verify. The MIT grant matches the upstream text. Local Markdown
  links/anchors, code fences, whitespace and git diff --check pass. The four Go
  documentation snippets were compiled during the preceding README update.
- Prepare five CGO-disabled CLI archives from the clean release commit:
  Windows amd64, Linux amd64/arm64 and macOS amd64/arm64. Package the root MIT
  license, Go runtime license and the linked modules' license/notice files.
  Gate publication on build metadata, archive contents, Unix executable modes
  and the packaged Windows CLI version banner. SHA256SUMS.txt is included only
  for download integrity; no additional source checksum scheme is introduced.
- The authorized release uses a new annotated v0.17.1 tag and GitHub Release.
  Download verification and the resulting publication status are reported on
  the release page and in the task reply. Linux/macOS runtime acceptance and
  live provider calls are separate from local cross-compilation evidence.

## 2026-10-28 — CommandCode v0.7.10 selective catalog sync

- Compared upstream `patlux/pi-commandcode-provider` at
  `63c8e5ccea73850266c34e655d3a76039316d4aa` (v0.7.10 / CLI 1.88.0)
  against pi-go master `d831f6c39848d326962d82b2f8cf0f5b3805c295`.
  Updated the CLI snapshot header and seven newly documented model capability
  entries; added eight upstream reviewed display prices. Existing prices and
  tiers remain unchanged. This is an estimate snapshot, not current billing.
- Removed the two retired free Ling entries and MiMo 2.5/2.5 Pro from built-in
  models, capabilities and estimated prices. No alias from a retired/free model
  to a paid model is introduced. Existing user configurations and cached catalogs
  are not deleted; a stale cache can still contain retired IDs until refresh.
  Newly documented models remain discovery-only:
  no guessed native endpoint, context limit or automatic model switch is added.
  Kimi K3.1 Pro remains text-only/non-reasoning with unknown capabilities until
  upstream publishes metadata. DeepSeek `off` keeps pi-go's existing normalized
  mapping rather than forwarding an `off` reasoning parameter.
- This is not a 502 fix claim. JZ uses relay-owned native adapter contracts;
  no generate fallback, retry count, auth, quota query or relay contract changes
  are included. Upstream v0.7.8's JS iterator fallback and quota cancellation
  mechanisms are not copied blindly into Go. pi-go's public QueryQuota already
  supplies one default 15-second context deadline; custom transports ignoring
  context are not newly claimed as covered.
- Regression coverage checks discovered model capabilities/prices/header,
  endpoint preservation, conservative unknown capabilities, no implicit model
  enablement and retired metadata removal. Live provider/model calls, CI,
  release and deployment are not part of this batch.
- Cloud offline validation (Go 1.26.10, live flags disabled): CommandCode focused
  tests and root `go test -mod=readonly -count=1 ./...` passed. After adding the
  JSON discovery regression, full provider `-race` passed (28.800s) and root
  `go vet -mod=readonly ./...` passed. Independent source review accepted the
  final changes. Windows and real providers were not run. Publication is a
  non-CI-matching branch only: a PR targeting master would trigger the existing
  workflow, so no PR, merge, release or downstream dependency bump is performed.

## 2026-10-28 — v0.17.2 combined release candidate

- Combined reviewed catalog commit `271976a438687c22dfe3f672ad1ec674b9fa5446`
  and Responses diagnostic commit `77484dac232f820cc8e973d94adfdc7c00a931a8`.
  Prepared package/examples version 0.17.2 and candidacy/compatibility notes.
- Go 1.26.10 cloud offline validation: root tests and vet, provider race tests
  (29.253s), module verification, examples tests and vet passed. Independent
  combined source review found no blocking issue. Live calls and Windows/macOS
  execution were not performed. The candidate is not evidence of a resolved
  real-world 502 or deployed downstream applications.
- Publication remains blocked by the no-CI requirement: master PR/push triggers
  the existing workflow. No workflow was disabled and no feature-branch tag was
  substituted for mainline integration. The current connector also lacks tag/
  release upload operations, and the cloud GitHub CLI is not authenticated.
  This candidate branch is not a published version and must not be used as a
  claim that v0.17.2 already resolves through the module proxy.

## 2026-10-28 — Owner-requested manual-only GitHub CI

- Removed automatic push and pull_request triggers from the sole Go tests
  workflow after the owner explicitly requested disabling automatic GitHub CI.
  Retained workflow_dispatch and all existing jobs/permissions unchanged; no
  manual run is dispatched. This applies only to pi-go, not account-wide settings.
- Parsed both YAML versions and verified that only the event triggers change.
  The configuration-only commit precedes release-candidate integration; GitHub
  evaluates push workflows from the pushed tip, whose triggers exclude push.
  No skip marker or temporary workflow-disable/re-enable race is used.

## 2026-10-28 — Final candidate and manual-only CI integration

- Combined the reviewed v0.17.2 candidate with CI-only master commit
  `028cb4e2305b78719487853447e34947f6c82d1e`, preserving both parent histories.
  Compared the final source tree: runtime and tests are unchanged from the
  candidate validated above; only CI configuration and status documentation
  differ. The sole workflow remains manual-only; no run is dispatched.
- This resolves the CI configuration blocker, not the release credential gap.
  A tag, release assets and downstream module resolution remain separate gates.

## 2026-10-28 - Publish the v0.17.2 source tag

- Fast-forward the clean local master to the reviewed remote integration.
  Keep the prepared package and examples versions at 0.17.2; update the release
  notes from candidate status to source-tag distribution.
- Windows Go 1.26.4, GOWORK=off, all live-provider gates disabled: focused
  CommandCode catalog and Responses error-evidence regression tests pass.
  Root and examples both pass go test -mod=readonly ./... -count=1,
  go vet -mod=readonly ./... and go mod verify. Full provider race tests pass;
  a compiled CLI reports pigo 0.17.2. git diff --check passes.
- Publication uses an atomic push of master and a new annotated v0.17.2 tag,
  followed by remote-ref and direct Go-module resolution checks. Existing
  public tags remain intact. No additional source or artifact hashes are added.
- This request publishes the source tag without a GitHub Release or binary
  downloads. GitHub CI remains manual-only and no workflow is dispatched.
  Non-Windows execution, live providers and downstream deployment are outside
  these local validation results.

## 2026-11-01 — Exact JSON evidence for completed tool calls

- Added optional original argument JSON on completed tool calls from Responses,
  OpenAI chat completions, DeepSeek chat, Anthropic Messages and Command Code.
  The in-memory evidence survives event/result cloning. Existing argument maps
  and serialized conversation formats keep their existing representation.
- Added local transport regressions for large integers and duplicate keys in
  terminal arguments, including a Responses call with no argument delta.

## 2026-11-02 — Kiro watchlist and tested development integration

- Carry #50's explicit upstream/plugin watchlist into the Kiro development stack,
  preserving its source commit as an additional remote merge parent. Existing
  instructions and other plugin baselines are unchanged; Kiro implementation
  wording now points to docs/kiro-followup-status.md instead of saying absent.
- Read-only upstream main remains dd93f2e (package version 0.12.3), dated
  2026-10-29 22:21:36 UTC. No new main delta observed; tag-list API unavailable.
- Latest code at #111 includes independent synthetic Go/race tests of scoped
  credentials, explicit buffered/live-text registration and internal mixed
  text/tool worker. Final code full-repository race and vet passed with Go1.26.2;
  this documentation integration leaves code blobs unchanged.
- Draft branches only: no master merge, release/deployment, automatic login,
  real credential/service/tool calls or CI dispatch. Community protocol evidence
  and fake tests do not prove service authorization, account/model availability,
  actual billing or complete upstream compatibility. See status document for
  exact tests, source links and remaining public mixed-Agent integration.

## 2026-11-02 — Explicit incremental text/tools registration

- Upstream baseline remains pi-provider-kiro dd93f2e / package0.12.3 (main checked
  at 05:09 UTC; no new main delta). Added a distinct public incremental config,
  options and API registration; existing buffered/live-text modes retain behavior.
- Uses live provisional text plus completed-only tool lifecycle. Actual no-tools
  and tool-enabled fake Agent tests now cover Auto/session opt-in, permission deny,
  cancellation upon ToolCallEnd, truncation/late Close, exact9007199254740993 raw
  argument replay and execution. All tools/services in tests are local fakes.
- Official Go1.26.2, cached deps, live flags disabled: focused registration/Agent
  race repeated three times passed1.172s; final code full-repository race passed
  (pigo30.590s), go vet, gofmt and diff checks passed. Parent review was independent
  static inspection, not a second execution. See incremental registration doc.
- This remains a draft development branch, not master/release/deployment. No
  real credentials, service calls, CI dispatch or automatic login/model setup.
  Preview text cannot be revoked; a tool lifecycle event is never authorization
  without complete successful result and current schema/permission/context checks.

## 2026-11-02 — Offline refresh response semantics

- Compared pinned pi-provider-kiro dd93f2e src/oauth.ts and test/oauth.test.ts.
  Independently implemented supplied-response parsing for IDC, desktop and
  external-IDP families, keeping exact scope and actual expiry separate from
  refresh-after. Unknown/new fields and missing expiry deliberately reject.
- No credentials discovered, sources replaced, endpoints called, timers started,
  persistent access created, CLI/IDE data read/written or real refresh performed.
  Only omitted desktop/IdP refresh fields retain caller's old token; null/empty
  reject. IDC needs rotation evidence. Secrets are excluded from JSON/fmt output.
- Official Go1.26.2 offline/cache-only: focused refresh race x3 passed1.052s;
  full repository race passed (pigo30.731s), go vet/gofmt/diff checks passed.
  Parent review was static, not independently executed. Synthetic tests cover
  expiry bounds/overflow, scope/family conflict, unknown fields and redaction.
- Still a draft-only internal parser, not automatic OAuth/login or proof of
  endpoint/account/billing compatibility. No CI or real service calls.

## 2026-11-02 — Scoped offline catalog selection

- Kept pinned management/models baseline dd93f2e. Bound parsed catalog evidence
  to caller-asserted profile/region and observation time; selection requires
  explicit current time/maxAge and alias versus exact-service-ID namespace.
- No fetch/cache/default model/registry/entitlement claim. A concrete alias can
  equal another record's service ID despite unique aliases, so lookup never
  guesses namespace. Returned limits/schema/metadata are detached; no properties
  or prices inferred from missing fields. Catalog JSON/fmt hides private state.
- Official Go1.26.2 offline/cache-only: final focused race x3 passed1.080s;
  full-repository race passed (pigo30.947s), vet/gofmt/diff checks passed. Four
  original tests cover scope/freshness boundaries, explicit namespace ambiguity,
  nil limits, 16 concurrent mutable return copies and invalid observations.
  Parent review was static only. No live credentials/service/CI or deployment.

## 2026-11-02 — Offline scoped management request assembly

- Compared dd93f2e management.ts requestManagement/ListAvailableModels wire shape.
  Added a construct-only GET for explicit region/profile/origin/UA, safe query
  escaping and one explicit scoped credential callback. No default network,
  profile/region discovery, token inference, retries or registry changes.
- Existing User-Agent validation is extracted unchanged for both request paths.
  Management requests intentionally omit POST target/content and runtime headers.
  Returned headers/URL remain sensitive and must not be logged or redirected.
- Go1.26.2 cached dependencies/live flags disabled: focused catalog/client-header
  race x3 passed1.041s; full repository race passed (pigo31.546s), vet/gofmt/diff
  checks passed. Three original tests exercise all auth families, malformed
  inputs, scope/cancellation/error and callback option-mutation isolation.
  Parent review was static only. No real credential/service/CI/merge/deployment.

## 2026-11-02 — Injected catalog response pipeline

- Joined explicit scoped management request to one injected executor and bounded
  JSON response handling, then Close-validated explicit clock and scoped catalog.
  No default network, profile probes, retry/cache/automatic model registration.
- HTTP200/MIME/UTF8/encoding/length guards, maxBytes+1 EOF probe, sticky reader
  errors and no-progress limit apply. Response+transport error still closes body;
  Close failure/cancellation discard catalog. No raw errors or tokens logged.
- Go1.26.2 offline/cache-only: final focused race x3 passed1.042s; final full
  repository race passed (pigo31.108s), vet/gofmt/diff checks passed. Four original
  tests cover blocked Close ordering, status/header/length/read failures, exact
  budget, final bytes+error/wrappedEOF, nil response/body and clock rejection.
  Parent review was static, not a second execution. All I/O is injected fake.
- Scope/timestamp are assertions and executor must cooperate with cancellation;
  no real service/account/billing validation, CI, merge or deployment.

## 2026-11-02 — Explicit public catalog observations (draft, stacked on #117)

- Baseline: pi-provider-kiro v0.12.3 dd93f2e; independently authored public-facing
  cases extend management/models scenario coverage, without copying upstream code.
- Added explicit FetchKiroModelCatalog config with required injected scope/auth/
  executor/clock/limits; opaque immutable catalog and observation values; Models
  and explicit alias/service Select require exact scope and freshness. Detached
  raw getters preserve metadata/numeric evidence, absent limits stay absent.
- Shared prior scoped guard/clone semantics; no automatic registration/cache/
  login/refresh/default model/network. Scope/time are caller assertions, not
  entitlement. Runtime service-ID routing and real service acceptance remain open.
- Three external-package Go tests cover no registration, callback lifecycle and
  redaction, Close-before-clock, nil/zero values, deep-copy/concurrent getters,
  scope/age rejection, exact large integers, missing limits and opaque formatting.
- Actual validation: official Go 1.26.2 cloud, cached dependencies, live flags off;
  final focused public/scoped race x3 passed (1.047s); final full repository
  go test -race ./... passed (pigo 31.687s; other packages passed/cached);
  go vet ./..., gofmt and git diff --check passed. Parent independent static
  review found no definite blocker; it did not independently execute tests.
- No CI dispatch, real account/network/tool execution, master merge or deployment.
  See kiro-public-catalog-observation.md for public API ownership/trust boundaries.

## 2026-11-02 — Explicit catalog service-ID to runtime regression (draft)

- Stacked on #118; no production implementation change. Independently authored
  scenario from the upstream dd93f2e distinction between host alias and kiroModelId.
- Synthetic catalog intentionally has overlapping alias/service namespaces.
  Public Select(ServiceID) feeds caller-owned Model.ID; public registration and
  StreamSimple preserve exact ID in current/history wire paths and final result.
  Catalog/runtime are each invoked once; registration invokes neither callback.
- Documentation now explains manual ServiceID mapping and explicitly excludes
  automatic alias resolution, per-dispatch catalog freshness, capability defaults,
  entitlement, model registration and real endpoint compatibility guarantees.
- Actual official cloud Go 1.26.2/cached dependencies/live flags disabled:
  final catalog-runtime/public focused race x3 passed 1.042s; full repository
  go test -race ./... passed (pigo30.407s, others passed/cached); go vet ./...,
  gofmt and diff check passed. Parent independent static review found no definite
  blocker; no independent execution by reviewer. No real service or CI dispatch.

## 2026-11-02 — IDC-only offline refresh request (draft)

- Baseline community oauth.ts dd93f2e442–467 plus official AWS CreateToken docs
  read on 2026-11-02. Independently implemented standard-partition regional OIDC
  JSON POST with explicit clientId/clientSecret/refreshToken/refresh_token grant.
- Required explicit UA, context and encoded-byte budget; no arbitrary endpoint,
  China/GovCloud/isolated suffix guessing, default client, executor, credential
  source, token import, refresh execution or persistence. Region syntax does not
  prove endpoint availability/account authorization. This is an internal helper.
- Input options JSON/fmt hide secrets; fixed errors; request body intentionally
  contains secrets and must not be logged/persisted/redirected. GetBody absent
  removes only convenience replay, not a transport-enforced retry guarantee.
- Three original tests cover exact fields/URL/headers, escaping and exact byte
  budget, secret-free diagnostics, invalid inputs/partitions and cancellation.
- Official cloud Go1.26.2, cached dependencies/live flags off: focused IDC race x3
  passed1.032s; full repository race passed(pigo31.276s, other packages passed/
  cached); vet/gofmt/diff passed. Independent parent static review found no definite
  blocker, not independently run. No CI/master merge/deploy/live service calls.
- Follow-up: request and prior response parser remain disconnected; any sender,
  source mutation, client registration or actual login requires separate review.

## 2026-11-02 — Injected IDC attempt, fake-only (draft)

- Stacked on #120. Joins strict request and response components with exactly one
  explicitly injected executor call; no default client, redirect/retry, login,
  client registration, source mutation, credential persistence or live call.
- Validates scope-region and policy before executor; reuses existing bounded JSON
  transport reader for status/MIME/encoding/length/EOF/Close. Clock/parser run only
  after successful Close; all failures return zero secret result and fixed error.
- Four original test functions cover input snapshot mutation, exact request,
  close-gated rotation/expiry, blocked Close withholding result, response+error,
  302/401/header/length/budget/read/close/cancel/JSON/missing-rotation/clock failures
  and preflight preventing executor. Rejected response bodies are not read.
- Sources retained: official AWS CreateToken and community dd93f2e. Required IDC
  rotation remains a documented conservative difference from optional official
  refreshToken. Callback behavior is trusted, cooperative; plaintext memory is
  not zeroized and bodies must never be logged/redirected. No account proof.
- Actual official cloud Go1.26.2/cached deps/live disabled: final focused race x3
  passed1.035s; full repository race passed(pigo30.669s, others passed/cached);
  vet/gofmt/diff passed. Parent independent static review found no definite
  blocker, not independently executed. No CI/master merge/deployment.

## 2026-11-02 — Explicit caller-owned public IDC refresh (draft)

- Stacked on #121. Public RefreshKiroIDC requires explicit scope/client inputs,
  request/response budgets, lifetime/skew, clock and executor. Opaque result getters
  return copied in-memory credentials/rotation/expiry only. No defaults, source
  updates, persistence, login, registration, retries or live service calls.
- Public typed error exposes ExecutorInvoked only: false means this helper did
  not invoke it; true means server-side rotation outcome may be unknown even if
  no/partial/all body was read or response/Close/parse/clock failed. Not a sent flag
  or retry-safe classification. No raw error retained, no old-token validity claim.
- Three original external-package tests cover success/getters/ownership, opaque
  fmt/JSON (callback config deliberately not JSON-serializable), errors.As/nil/zero values,
  preflight no dispatch, and each indeterminate rotation stage with single call.
- Final official cloud Go1.26.2/cached dependencies/live flags off: focused public
  IDC race x3 passed1.038s; final full repository race passed(pigo31.042s, others
  passed/cached); vet/gofmt/diff passed. Independent parent static review found
  no definite blocker; it did not independently run tests. No CI/master/deploy.
- Required response rotation remains a conservative IDC subset. Executor must
  not redirect/retry/log and must honor cancellation; plaintext is not zeroized.
  Real endpoint/account/client compatibility remains unverified.

## 2026-11-02 — Confirmed refresh expiry-anchor correction / #122 audit

- A new reproducer FAILED against the prior implementation: fake Close advanced
  clock by ten minutes and ExpiresAt became06:10 instead of conservative06:00.
  RFC6749§5.1 lifetime is from response generation, not client Close completion.
- Fixed only IDC attempt timing: validated clock before executor, second clock
  after successful Close, reject backward/invalid/end>=expiry; parse anchored to
  start. The pure parser and exported struct layout remain unchanged. Clock's
  success-path invocation count changes1→2 and is explicitly documented.
- Post-dispatch expiry/time failures retain ExecutorInvoked=true (rotation may
  already have happened); pre-clock invalid/error/cancel are false with0 executor.
  RefreshAfter may already be due, never extends expiry. Trusted same-clock
  semantics are required; this is conservative, not actual server timestamp proof.
- Added Close-delay reproducer and external boundary matrix for prezero/error/
  year/cancel, ending backward/exact-expired/beyond/within; updated existing
  callback-count tests. Fixed stale 'no public API' docs and recorded a scoped
  audit of rotation omission, extension fields, secrets and real-service gaps.
- Final official cloud Go1.26.2, cached dependencies/live disabled: focused IDC
  race x3 passed1.055s; full repository race passed(pigo30.711s, others passed/
  cached); vet/gofmt/diff passed. Independent child confirmed defect and static
  correction review, no separate test run. No CI/master merge/deploy/live calls.

## 2026-11-02 — Explicit successful IDC omission retention (draft)

- Sources: official AWS CreateToken optional refreshToken and RFC6749§6 optional
  replacement. Added separate RefreshKiroIDCWithOptions and controlled
  WithKiroIDCRetainMissingRefreshToken; zero/duplicate option rejects before any
  callback. Strict old entry and no-option behavior/config layout remain unchanged.
- Only fully validated successful responses with genuinely absent refreshToken
  retain the exact request-token snapshot. Explicit null/empty/duplicate/invalid
  or transport/Close/clock/expiry failures still zero-return. Explicit returned
  token wins even if identical; RetainedRefreshToken reports provenance only.
- No source manager/lock/cache/storage/login. Caller must serialize same-generation
  refresh across processes and fence late results against the actual generation;
  a failed/unknown attempt never proves old-token validity or retry safety.
- Three original external test functions compare strict/no-option/opt-in field
  cases and late failures, no-callback invalid options, and16 independent fake
  tokens concurrently with no global state. This is not same-token concurrency
  safety. Independent child static review found no blocker; existing docs updated
  to distinguish default strict path from new explicit retention option.
- Actual official cloud Go1.26.2/cached dependencies/live off: focused response/
  attempt/public/retention race x3 passed1.064s; full repository race passed
  (pigo30.456s, others passed/cached); vet/gofmt/diff passed. Reviewer did not
  independently execute. No real account/service/CI/master merge/deployment.

## 2026-11-02 — Offline desktop refresh request subset (draft)

- Pinned community oauth.ts dd93f2e endpoint constant/desktop branch supplies
  POST prod.<region>.auth.desktop.kiro.dev/refreshToken with JSON refreshToken.
  This is not official public API guarantee or proof of service-use permission.
- Independently implemented internal constructor, bounded safe hostname shape,
  asserted profile scope, explicit UA/token/context/escaped-byte budget. No default
  desktop impersonation, arbitrary URL, IDC secrets, bearer header, executor,
  discovery/login/CLI/desktop launch, persistence or real service request.
- Three original tests cover exact URL/method/headers/one body field, escaped byte
  budget, redaction, invalid inputs, cancellation and value snapshot ownership.
  Secret body must not be logged/redirected; GetBody=nil not transport enforcement.
- Actual official cloud Go1.26.2, cached deps/live off: focused desktop request
  race x3 passed1.036s; repository race passed(pigo30.714s, others passed/cached);
  vet/gofmt/diff passed. Independent child static review no blocker, no independent
  execution. No CI/master merge/deploy. Desktop attempt remains next separate step.

## 2026-11-02 — Single injected desktop refresh attempt (draft)

- Stacked on #125; exact pinned desktop family, no official API guarantee.
  Connects explicit request to shared bounded JSON/HTTP reader and existing desktop
  parser; no new network client, login, token discovery/source update/persistence.
- Preflight/start-clock before executor; one explicit invocation; successful Close
  before end-clock/parser. Anchors expiry to start, rejects rollback/exact-expiry.
  Bool means executor invoked, not sent/receipt/retry safety. Every returned failure
  after invocation is true+zero result, including profile mismatch/Close/time.
- Returned profile must match asserted scope; absence is not account proof.
  Rotation absence retains exact prior token under existing desktop semantics;
  null/empty reject and explicit rotation wins. No same-generation source locking.
- Three original table-driven tests cover exact body/snapshot/scope/rotation/
  Close-delay expiry, profile mismatch/null, token null/empty, transport/redirect/
  headers/length/budget/Close/cancel/clock/expiry failures and preflight no invocation.
- Official cloud Go1.26.2, cached deps/live off: focused desktop attempt race x3
  passed1.039s; full repo race passed(pigo30.585s, others passed/cached); vet/gofmt/
  diff passed. Independent child static review no blocker, not independent run.
  No real credential/service/desktop/CI/master merge/deploy operation occurred.

## 2026-11-02 — Explicit caller-owned public desktop refresh (draft)

- Stacked on #126; public RefreshKiroDesktop config supplies all scope/token/UA/
  budget/time/executor inputs. Opaque result and typed ExecutorInvoked error carry
  no implicit source/cache/login/store. Scope/retention are not identity/validity
  guarantees. The community endpoint is not an official third-party API promise.
- Three original external tests cover actual wrapper fake wire, omission/rotation/
  explicitly returned same token provenance, profile/expiry, secret getters/copies,
  fmt/JSON/zero/errors.As, no-dispatch and post-dispatch unknown failures and Close.
- The added same-token test initially had an incorrect expected NEW value and
  failed; corrected only that test expectation. Production parser correctly
  returned the explicitly supplied old value with retained=false throughout.
- Final official cloud Go1.26.2/cached dependencies/live off: focused public
  desktop race x3 passed1.041s; full repository race passed(pigo30.858s, others
  passed/cached); vet/gofmt/diff passed. Independent child static review found no
  blocker, not independently executed. Prior pre-final runs are not final passes.
- No real service/account/desktop/credentials, CI dispatch, master merge or deploy.
  Caller controls generation serialization and must prohibit retries/redirects/
  secret logging; arbitrary callback behavior remains trusted/cooperative.

## 2026-11-02 — Caller-owned refresh/runtime handoff and convergence (test-only)

- Stacked on #127; no production change. Both public IDC/desktop refresh APIs feed
  a caller-authored expiry-check Credentials callback into public registration/
  StreamSimple. Synthetic frames/fixture helpers only support data construction.
- Six scenarios assert access only in Authorization, no access in JSON or other
  headers/URL, no refresh/client secrets in runtime URL/body/headers; wrong scope
  and exact expiry suppress runtime. Valid-then-expired second request does not
  renew implicitly. One refresh only; registration invokes no source or executor.
- Independent child review found a coverage wording gap, not a production leak:
  added explicit non-Authorization access-header/URL assertions and corrected the
  helper description before final tests. No concurrency blocker found.
- Final official cloud Go1.26.2/cached deps/live off: focused handoff race x3
  passed1.041s; final repository race passed(pigo30.588s, others passed/cached);
  vet/gofmt/diff passed. Independent review was static, not separately run.
- Read-only upstream main rechecked06:43UTC: stilldd93f2e/v0.12.3 committed
  2026-10-29T22:21:36Z. No new main delta. Existing tag-list gap is not hidden.
- Added convergence review: connected APIs/test limits, live/identity/executor/
  raw-persisted-history/IdP/extension gaps, and integrated review before further
  features. No real service/account, source store, CI/master merge/deploy.

## 2026-11-02 — v0.18.0 integration and release validation

- Integrated the original heads of 91 open PRs from the Kiro and runtime stacks.
  Reconciled the Kiro retry formatting branches and the earlier adapter design.
- Review reproduced three defects. Kiro now checks the original conversation
  before a generic clone can lose content, Agent records context usage only
  on successful completion, and Codemode copies JSON without invoking an
  inherited descriptor getter. Added regressions through the public entry points.
- Windows amd64, Go 1.26.4, live flags off. Root and examples passed uncached
  race tests, vet and module verification. The external ToolCall compatibility
  probe passed. The Codemode prelude suite passed all 30 cases in both Node VM
  and the pinned QuickJS WASM runtime.
- Updated package and examples versions and documented the AgentEvent and
  ToolCall migration points. Kiro endpoint and account behavior remain unverified.

## 2026-11-03 — opt-in in-process receipt compatibility foundation

- Based on v0.18.0 master, independently of the incompatible #144 struct-field
  additions. Preserves old public layouts, Invoke, execution JSON and persistent
  formats; positional literal regressions cover the affected API surfaces.
- New direct Agent/Codemode wrappers return a bounded, separate host receipt
  handle. Central per-body reservation/sealing covers static/dynamic children,
  cancellation, panic and output-hook/projection failure. No report means
  unknown; pending calls remain observable after a canceled caller returns.
- Actual temporary-file commit test retains commit through hook failure; missing
  capture refuses before file I/O. Real Agent and checkpoint runners reject
  nested capture before receipt-required tool bodies, not before their own
  model calls or persistence bookkeeping. No automatic retry or write defaults.
- Offline Go1.26.2: full root tests and vet passed; Agent, Codemode, native adapter
  and collector race tests passed, including independent review. No CI, live
  service, local user computer, Eino dependency or deployment was used.
- Deliberately not a full #144 repair: no model-loop/session/checkpoint receipt
  persistence, JS receipt projection, restart recovery or write-stack replay.
  #144 and dependent write-tool merge blockers remain pending separate phases.

## 2026-11-03 — session-backed direct receipt calls (phase 2)

- Adds an explicit single-use direct-call binding over #150, not a model-loop
  journal or checkpoint extension. Old public types, runtime payloads, headers
  and JSONL batches are unchanged. No write-tool-stack migration or defaults.
- Atomic intent and final pairs each preserve legacy Pending:true. New reads
  validate scope/epoch, branch/head, fence/intent IDs, phase and bounded facts.
  Pending histories refuse execution after restart. Only the original live
  handle can be finalized; no replay, reset or caller-invented effect receipt.
- Tests cover real file commit/hook failure, Codemode child effects, lost ACK
  before/after execution, authorization changes during permission and storage,
  concurrent binding CAS/Finalize, fork, compaction, bounds and malformed pairs.
  Existing torn-tail repair can trim a partial final; surviving intent remains
  fenced. A newline-terminated corrupt batch is rejected instead.
- Actual current-writer/baseline-5dcf3c4-reader probe passed: old JSONL reader
  browses both custom receipt payloads, old PrepareRun returns ErrInterrupted.
  New reader also accepts valid legacy runtime history. This is read
  compatibility, not permission for unaware old runners to resume new writes.
- Offline cloud Go1.26.2: full root tests/vet and five relevant race packages
  passed; independent review and receipt-focused race passed. Actual checkpoint
  nesting test rejects this mode before business/receipt I/O, while accurately
  allowing checkpoint's own prior persistence bookkeeping.
- Limits remain: effects and log append are not one transaction, pending intent
  lacks live partial facts, durability follows Storage, and crashes may need
  external investigation. #144/write stack, model-loop receipt persistence and
  checkpoint recovery remain separate blocked work. No CI/live service, local
  user computer, extra dependencies, merge or deployment.

## 2026-11-03 — opt-in non-persistent model receipt runs (phase 3)

- Adds Runner.RunWithReceipts without changing old public layouts, snapshots,
  events, Invoke or journal formats. A private mode parameter reaches the loop;
  no inheritable context grant can turn legacy nested execution into this mode.
  Legacy Runner/Engine and checkpoint entries block capture; non-nil journals
  are rejected before a new receipt run starts. No durable model recovery added.
- Registration closes in the actual runner goroutine before done, while pending
  host/child bodies retain their original handle until they really exit.
- Error scheduling is intentionally unchanged. A regression actually appends to
  a file twice after a committed first-call error: the model can call again and
  both receipts survive. Capture is not a retry barrier, idempotency policy or
  automatic write safety. Host policy remains separate, not implicitly granted.
- Offline cloud Go1.26.2: full root tests/vet, relevant six-package race and
  independent review/race passed. Tests include real file/hook/panic/cancel,
  dynamic model turns, cumulative capacity, permissions, Codemode late bodies,
  nested legacy/checkpoint rejection and concurrent same-Runner handle isolation.
- Temporary composition with #149 filesystem production code showed a small
  migration: require capture before create/replace/edit I/O, report immediately
  after publication and remove #144 LocalEffect fields. Five integration race
  tests passed through real model/Codemode/session direct paths. This experiment
  did not port the entire old filesystem test suite or docs, and is not a
  published filesystem migration or permission to merge that stack.
- Model session/checkpoint persistence and write-stack replay remain separate;
  no large recovery framework, defaults, dependencies, CI, user-local computer,
  live model, merge or deployment was introduced.

## Compatible filesystem integration (2026-11-03)

- Reintroduced the complete #149 filesystem package on #152 without #144's
  incompatible public fields. Production migration changes only the three write
  entry guards and shared post-publication receipt reporting. Core defaults,
  dependency manifests and CI workflows remain unchanged.
- Preserved all original 80 tests and two examples; migrated commit assertions
  to actual immutable host receipts. Added five model/Codemode/direct-session/
  legacy-entry integration tests and documented the superseded PR topology.
- Offline Go1.26.2 full root tests and full root race passed, as did vet,
  examples tests, module verification and Windows/Darwin test compilation
  (compilation is not runtime/platform support). Independent fstools race passed.
- Historical unkeyed public struct literals compile. A real create_file call
  persisted via the direct session binding was reopened by the old baseline:
  it browsed both raw receipt payloads and refused continuation with ErrInterrupted.
- Review caught a potentially vacuous legacy edit guard fixture; it now uses a
  valid existing source, while replace uses different existing content. Both
  assert refusal and unchanged bytes. Permission/socket fixtures may skip under
  restricted OS privileges; no privilege bypass or external service was used.
- Nonpersistent model operation and repeated-call risks remain explicit. No
  model journal/checkpoint recovery, automatic retry safety, merge, CI,
  deployment, user-local execution or Eino dependency was introduced.

## Explicit host-executor tool adapters (2026-11-03)

- Added opt-in run_command/run_python factories in agent/exectools. They accept
  trusted host functions, never discover/start a process or install a runtime.
  Only bounded args/source are model-controlled; backend, tenant, environment,
  filesystem/network and actual resource/termination policy remain host-owned.
- Require capture/current permission before dispatch; recheck before output.
  Synchronous deadlines request cancellation, not kill/reap guarantees. Shared
  bounded stdout/stderr writers seal on return/panic, discard excess without
  accumulating it, and safely encode invalid UTF-8/control bytes with flags.
  Nonzero exits preserve bounded Agent diagnostics with IsError; Native keeps
  its existing rejection contract. Backend errors expose only safe messages.
- No inferred commit on start/success, no retry or sandbox claims. Explicit host
  reports survive downstream failure. No presets, old structs/Invoke, runtime
  scheduling, dependency manifests, persistence formats or CI workflows changed.
- Mock/public API tests cover strict raw and host arguments (including null,
  case aliases and duplicate keys), budgets, concurrent output/tenant closures,
  late writes, cancellation/deadline versus noncooperative body exit, explicit
  commits through error/panic/hook/validation, Native leaf policy and model runs.
- Offline Go1.26.2 full root tests, relevant six-package race, vet, module verify,
  historical unkeyed literal compilation, Windows/Darwin test compilation and
  independent mock race/review passed. Cross-compilation certifies no backend.
- Corrected receipt documentation's pre-#153 migration-status sentences. No
  actual shell/Python backend, external service, install, live model, CI,
  user-local execution, merge or deployment was performed.

## 2026-11-03 - User-local PR review and v0.19.0 preparation

- Execution was verified on the user's JACKIE-LAPTOP Windows host. The desktop
  project inventory identified V:\gitdownload\pi-go as local; that original
  checkout was clean and remained unchanged. Work used an isolated checkout in
  the Codex task directory and the host's existing WSL2 distribution.
- Reviewed #150 -> #151 -> #152 -> #153 -> #154 at their current heads, plus
  independent docs #138/#142. #153 replaces #139-141, #143 and #145-149;
  #144 is excluded. Root and agent AGENTS.md were read; no .agents directory
  exists in this repository.
- Local codex review found one concrete regression: receiptcapture.Block(nil)
  panicked before the Engine's existing context normalization. All five public
  Engine entry cases reproduced it. The fix preserves non-nil contexts and
  normalizes nil before applying the block. Five regression cases now pass.
  A focused local codex re-review found no actionable issue; its own test attempt
  was blocked by its read-only sandbox, so the tests were run separately below.
- The fix was pushed to #152 as 9906351d580fb2651df50d714bd6fd56f3d5ca84.
  The local integration carries the identical patch as 0f3bc31.
- Windows Go1.26.4: uncached full root tests, root vet and module verification,
  examples tests/vet, explicit compat_toolcall and external historical positional
  literals passed. After the fix, Agent/collector/checkpoint/Codemode/session
  tests and target-branch Agent/collector vet passed again.
- Local WSL Linux Go1.26.2: uncached full root race, full root vet and examples
  race passed. After the fix, all Agent/Codemode/collector packages passed race
  again. Locked dependencies came from existing Windows/WSL caches; no module
  versions were changed for setup. All live-service flags stayed disabled.
- A real new direct-call API wrote a file and JSONL receipts. The actual v0.18.0
  baseline then opened the log, browsed both receipt records and returned
  ErrInterrupted from its old PrepareRun. Old positional literals built with both
  baseline and current source. PR #142's exact new configuration compiled and
  constructed inertly without starting an MCP server.
- v0.19.0 notes, package version and examples pin are prepared. CLI/provider
  and examples tests passed after the version changes; the Windows CLI built
  successfully and reported pigo 0.19.0.

## 2026-11-04 - Reviewed stack integration for v0.19.0

- After the user's explicit confirmation, #150, #151, #152, #153 and #154 were
  merged in that dependency order, followed by independent docs #138 and #142.
  Stacked PRs were retargeted to master only after their prerequisites merged.
  Merge commits preserve the original histories, including the #152 fix.
- Master ee1061b27e230f3a757067b8d8291da868e8e069 has exactly the same file tree
  as the locally reviewed and tested integration before its version bump.
  No PR Approval was submitted. #144 and the superseded filesystem PRs were
  not merged; OCAI was outside this task.
- The release follows the existing five-platform CLI archive, bundled-license
  and SHA256SUMS convention. Archive building and final remote verification
  must use the merged release commit; a locally prepared version is not itself
  a published release or a downstream dependency upgrade.

## 2026-11-05 - Open filesystem and receipt PR review

- Reviewed #139–141, #143–149 against the v0.19.0 integration. #153 already
  contains the nine filesystem proposals. #150–152 provide the receipt API
  used by the integrated tools. The corrections below apply to that implementation.
- Reject duplicate and incorrectly cased filesystem argument names before
  execution. A raw read request could previously select the second of two
  different paths. A staging-time permission denial now reports that the
  destination was not published; a failed destination syscall remains uncertain.
- Bound Codemode local and remote status fields before recording or forwarding
  them. Agent, Codemode and its native adapter preserve typed-nil tool failures
  and their receipt facts without dereferencing a missing error payload.
- Windows Go 1.26.4 passed the offline root module tests and vet. WSL Linux Go
  1.26.2 passed race tests for agent, fstools, codemodetool, codemode and
  agent/session, including Linux publication regressions, and vet for the
  agent and codemode package trees.

## 2026-11-05 - Local SuperAgent prototype in the pi-go module

- Added `superagent.SuperAgent` and `cmd/pi-superagent` to this repository. The
  coordinator uses Pi's existing Runner for routing and per-task worker runs,
  and session JSONL storage for event admission, subscriptions and snapshots.
- Verified separate task histories, cross-source follow-up, admission deduplication,
  report replay and continued stdin service while workers run. Abruptly terminating
  a worker process leaves an interrupted event on reopen. No automatic replay of
  an already-started call is attempted.
- Windows Go 1.26.4 root tests and vet passed with live-model flags off. WSL
  Linux Go 1.26.2 race passed for the new package and command. Built and ran
  the Windows CLI offline. Production messaging connectors, external watchdog
  and remote execution are outside this local prototype.

## 2026-11-05 - SuperAgent tool outcomes and recovery review

- PiWorker uses the receipt-aware Runner and waits for admitted executors to
  exit, including late Codemode children. Reports distinguish a completed model
  run, a length-limited answer and a run that ended after tool errors. PiRouter
  rejects partial/failed decisions and honors the provider stream veto hook.
- A started worker with an uncertain result fences its task. Previously admitted
  follow-ups are blocked, including late attachments. Trusted reconciliation
  names the exact cause event and records a note; only fresh instructions can
  resume work. Offline reconciliation does not dispatch queued events.
- Reports retain host-reported local commits independently of tool success.
  Raw callback errors are available to the current host through FailureCause.
  CLI returns the journal error if its terminal write fails with stdin open.
- Verified real Linux create_file publication followed by a rejected result,
  persistent receipts, blocked follow-up after a process kill following an
  external write, and Windows byte-lock failure during the final append.
  Windows offline root tests and vet passed. Linux race passed for superagent
  and its CLI. Rebuilt the local Windows EXE and ran the offline demo.

## 2026-11-05 - Live SuperAgent task control

- Added run-targeted steering and cooperative cancellation, and live task
  subscriptions. Targets use task ID and the source event identity so an old
  command cannot steer or abort a later invocation. PiWorker polls steering
  through its native receipt-aware Runner hooks; queued controls settle as
  delivered or undelivered. Progress carries bounded, task-scoped phase updates.
- The local JSONL CLI supports status, steer, abort, subscribe and unsubscribe
  on the running process. A control error is reported for that frame. Steering
  responses correlate admission and delivery. EOF drains outstanding receipt
  responses before the final state is printed.
- Verified native one-at-a-time and all steering delivery, ordinary queued
  follow-ups, other-task isolation, late local commits after abort, review
  fences, stale targeting, slow readers, unsubscribe and full-duplex CLI I/O.
  Windows offline root-module tests and vet passed. WSL Linux race passed for
  superagent and cmd/pi-superagent. Built the Windows EXE and exercised the
  local demo and interactive control protocol without provider calls.

## 2026-11-05 - SuperAgent review fixes and Agent parity

- Reproduced the duplicate-path write using a local Responses server, the
  native receipt-aware Runner and Linux `atomic_replace`. The Agent now
  rejects an executable model batch with ambiguous wire arguments before
  dispatch. Google and Mistral also retain the original argument object for
  this check. All eight filesystem tools validate direct raw and parsed inputs.
  The regression checks both file contents and zero receipts, plus a successful
  one-path replacement.
- PiWorker counts top-level completions against the model's requested calls.
  Nested success and failure cases cover both `Terminate` and `FinishTurn`,
  including parent-linked commit receipts and the resulting review status.
- Failed and aborted Pi runs retain their accepted task transcript and
  delivered steering. The journal stores the failure status and receipts
  alongside that snapshot. Regression tests cover reopening the journal,
  operator reconciliation, a fresh follow-up, and progress callback failure.
- Windows offline root-module tests and vet passed. Linux race tests passed for
  agent, agent/fstools, internal/jsontext, pkg/pigo, superagent and cmd/pi-superagent.
  Rebuilt the local Windows EXE and ran its offline demo through IDLE.

## 2026-11-05 - Embedded SuperAgent controls and remaining review fixes

- The CLI dispatches input independently of stdout. Its ordered frame queue
  spills to OS temporary files during backpressure. A real tool regression
  blocks stdout with 300 status requests, aborts before file publication,
  and lets another task complete before output resumes. Broken-output cleanup,
  initial subscription progress at EOF and steering receipt delivery after a
  terminal journal write failure have separate coverage.
- Direct Go calls also cancel the worker with an unread progress subscription.
  Completed workers expose a finalizing phase until their report is committed.
  Canceling a subscription immediately frees its slot. Added an executable Go
  package example and made service embedding the first documented entry point.
- New tasks persist independent provider session IDs. Old journals retain
  their original provider affinity. PiWorker restores the selected model with
  current host configuration and routes JSON-shaped steering as ordinary text.
  Linux filesystem publication now releases staging before classifying a host
  permission panic, including a typed-nil error.
- Windows offline root-module tests and vet passed. Linux race tests passed
  for agent, agent/fstools, superagent and cmd/pi-superagent. The offline CLI
  demo reached IDLE. Changes are local.

## 2026-11-05 - SuperAgent library development plan

- Added [the staged development plan](superagent-development-plan.md) and
  linked it from the package README. It maps PDF sections 2–7 to nine
  milestones with dependencies, implementation boundaries and acceptance tests.
- The first executable slice connects a Worker report to the resident Router
  and a durable host reply. Unsolicited discovery of new business work and
  speculative execution are deferred. Recovery of authorized work and
  independent Watchdog supervision remain in scope.

## 2026-11-05 - Same-chat and snapshot optimization integration

- Combined draft #160 at `4670b5f` and all three #161 commits ending at
  `420209a` on an isolated branch targeting #160. Original branches and their
  #159 shared base remain unchanged and unmerged.
- Terminal correction evidence reads the same validated decoded snapshot;
  replay still checks saved bytes, and main/task/control authority boundaries
  remain intact. Added before-replay and read-only evidence assertions.
- Remote Go 1.26.10 offline tests/vet, full race, examples, repeated snapshot
  and correction recovery tests, two-task acceptance and original benchmarks
  passed. Independent review found no blocker. No CI, live provider, local
  computer, extra billed task, merge or deployment was used.
- Exact inputs, verification scope and limitations are recorded in
  [the integration report](superagent-m2a-snapshot-integration.md).

## 2026-11-05 - SuperAgent shared-state lease slice

- Based on M5a draft #169, added optional same-host SQLite domain leases and
  fenced coordinator append. Existing JSONL APIs and the single coordinator
  scheduler remain unchanged. Driver selection and remaining M6 scope are in
  [M6a](superagent-m6a.md); this is not multi-host or production validation.
- Read-only locked JSONL snapshots reject torn tails without repair. Atomic
  import retains legacy single/batch records, task/provider/delivery identities,
  subscriptions, private snapshots, reply attempts and external-effect review.
- Offline full tests, vet, full race, storage and integration race repeated 20 times,
  external examples tests/vet passed on Linux amd64 Go 1.26.10. Real subprocess
  contention, kill/recovery and SIGSTOP/takeover/SIGCONT fence old writers.
  Independent review's URI space-escaping finding was fixed and re-reviewed.
- No live provider/platform, production DB, user-local task, manual CI, merge or
  deployment. Shared files require a trusted private local directory. External
  effects still need exact-operation evidence; lease fencing cannot undo them.


## 2026-11-05 - SuperAgent fixed-device protocol slice

- Based on draft #170's exact tree, added optional host-authenticated fixed-device
  backend protocol with durable request identity, acknowledgments, query/stop,
  monotonic epoch fences and non-replaying restart. See [M7a](superagent-m7a.md).
- Two independent subprocess devices cover request/response loss and process
  death after effect before acknowledgment. Native Pi/M4/M6 integration covers
  new Worker input while waiting, coordinator replacement, review/quota retention
  and one original-call completion. Device credential canaries stay off the wire
  and out of model, snapshots, coordinator log, replies and device registry.
- Independent review fixes preserve receipt fingerprints across regressions,
  reject impossible replay transitions and persist restart uncertainty correctly.
- Explicit Create is provisioning only; Open never recreates missing state. No
  real devices, user computer, model/provider, new access credentials, extra paid
  execution, manual CI, merge or deployment. This is not fleet scheduling,
  device-global durable quota, a persistent runner or power-loss validation.
- Final Linux amd64 Go 1.26.10 root readonly tests, vet, full race, device race
  x20 and separate examples tests/vet passed. Optional full module inventory
  could not complete with GOPROXY=off because some indirect-module metadata is
  uncached; no dependency versions changed and no manual CI was requested.


## 2026-11-05 - SuperAgent read-only Watchdog slice

- Based on unmerged draft #171's exact tree, added independent diagnostic
  session/model memory and injected clock, with a persistent bounded patrol
  cursor and atomic evidence/recheck records on the existing fenced writer.
  See [M8a](superagent-m8a.md). No retry/cancel/send/new-task actions are exposed.
- Normal slow work, queue waiting, live tools, explicitly observed device
  disconnection, suspected model loops and review barriers remain distinct.
  Uncertain external effects keep review; stale results are historical only.
- Failing tests preceded implementation. Independent review corrected goal
  provenance, reconciled-ready handling, numeric model-memory fidelity, settled
  task classification and strict replay memory/source validation.
- Fake-clock, controllable-model, restart/cursor, append failure and actual
  sharedsqlite epoch-takeover fixtures are included. No provider/real device,
  user computer, extra paid execution, manual CI, merge or deployment was used.
- This is the read-only M8a slice, not an authorized repair executor, independent
  out-of-process control plane, automatic notification or full M8 completion.
- Final Linux amd64 Go 1.26.10 readonly tests, vet, full race, Watchdog race
  x20 and separate examples tests/vet passed. Optional full module inventory
  remains blocked by uncached indirect metadata with GOPROXY=off; dependencies
  are unchanged. Final independent rereview found no remaining blocker.


## 2026-11-05 - SuperAgent offline evaluation baseline slice

- M9a is stacked on unmerged draft #172's exact tree. Added a deterministic
  300-delivery/20-request experiment with separate expected routing and observed
  ledgers, simulated timing/budgets, five repetitions and a checked-in compact
  baseline covering full-result SHA-256. See [M9a](superagent-m9a.md).
- Two structural scheduling surrogates are implemented; two single-Pi prompt
  baselines and the pre-M1 historical baseline explicitly remain missing.
  Real model calls are zero and tokens are null, not estimated usage.
- Fixed-service Router load sweep covers 1/5/10/20/40 events per virtual minute,
  maximum batches 1/8 and five repeats. The synthetic bypass fixture reduces
  scripted calls but shows no completion latency improvement.
- Actual runtime tests separately cover 300 valid Submit attempts, input/report/
  reply identity, failure isolation, worker results, journal reopen deduplication
  and legacy Open compatibility. These are not wall-time model benchmarks.
- Independent review identified zero-stop event scheduling and correctness
  scoring gaps; both have regression coverage. No production runtime or journal
  format, dependency, provider configuration or default behavior changed.
- No user computer, live service/model/device, credentials, extra paid execution,
  manual CI, merge or deployment was used. This is not final M9 acceptance.
- Linux amd64 Go 1.26.10 readonly full tests/vet/race, focused evaluation race
  x5, separate examples tests/vet and deterministic CLI baseline checks passed.
  Final independent review found no remaining blocker. PowerShell was not
  available on this cloud verification host; no Windows validation is claimed.


## 2026-11-05 - SuperAgent batch/background composition acceptance

- Inspected draft #173 exact HEAD/tree before implementing the M4a deferred
  batch-main item. M6a already exposes BatchRouter plus Background through
  OpenMainJournal. Direct integration tests passed without a production change;
  no reproduced runtime defect or new constructor is claimed. See [M4b](superagent-m4b.md).
- Added public combined-constructor coverage for ordered mixed backlog and
  reversed decisions, original Worker/provider/delivery identity, private output,
  stop plus late commit, authorization/quota/stale capability and unknown barriers.
- Native Pi protocol acceptance now runs in single and batch main modes. Real
  same-host SQLite takeover covers held inference, stale-owner fencing, exact-ID
  backend recovery, no Start replay and terminal reopen deduplication.
- Corrected the outdated plan and M4a limitation; durable remote execution still
  remains out of scope. Production code, journal format and dependencies unchanged.
- No live model/service/device, user computer, Codex/extra paid execution, manual
  CI, merge or deployment. Linux-only verification and independent review results
  are recorded in the PR; no PowerShell or Windows execution is claimed.


## 2026-11-05 - SuperAgent public history admission security correction

- Based on draft #175 exact remote HEAD cc3bb8ef / tree bcef2e95. The public
  probe and tests-first single/batch regressions reproduced denied/noise text
  leaking through thread links, trigger JSON and ignored report backlinks.
- One immutable root-admission predicate now protects all history nodes, task
  associations, reports and offered refs. It preserves accepted/model-ignored
  input, authorized fanout, privileged non-noise history and host audit records.
- Regression coverage includes all ignored reasons, direct reference guesses,
  native Pi tools and saved snapshots, paginated graph traversal, cache/cursors,
  duplicate policy changes, reopen and pre-gate journal upgrade. See
  [history admission boundary](superagent-history-admission.md).
- Existing leaked model snapshots/derived content cannot be undone by filtering
  the index. The upgrade note requires trusted-host review of potentially
  contaminated sessions rather than claiming restart sanitizes them.
- Full readonly tests/vet, serialized full race (-p=1), examples tests/vet and
  focused race x20 passed. Independent security review found no blocker and
  passed focused race x3. Initial parallel race and one repeated stream-test
  run hit the unchanged pkg/pigo two-second deadline; base stream x20 and the
  final full race passed. No provider code/test changes hide this sensitivity.
- No journal schema/dependency change, live service/model/device, user machine,
  Codex/extra paid execution, manual CI, merge or deployment. Linux amd64 Go
  1.26.10 only; PowerShell/Windows/macOS execution is not claimed.

## 2026-11-05 - Correct GitHub edited-comment actor binding

- Full-stack review reproduced a rejected legitimate edit on draft #175's tree:
  authenticated editor 7 editing author 8's comment failed the original
  sender-equals-author check. GitHub documents [editing others' comments](https://docs.github.com/en/communities/moderating-comments-and-conversations/managing-disruptive-comments#editing-a-comment)
  and identifies the initiating actor as [sender](https://docs.github.com/en/webhooks/webhook-events-and-payloads#the-sender-property).
- Only supported edited issue/review comments may have distinct author and
  sender. Principal stays bound to authenticated sender; author must independently
  be a canonical positive uint64. Creation/submission equality is unchanged.
- Red regressions preceded the fix. Tests cover both comment types, editor allow/
  deny policy, host intent, spoofed sender/target/object, malformed author, durable
  replay and edited own-message echoes without cross-type ID collisions.
- Independent read-only review and channel tests found no blockers. Linux amd64
  Go 1.26.10 readonly full tests/vet/race, focused GitHub race x20 and separate
  examples tests/vet passed using cached modules with live-test flags disabled.
- The parallel history-admission visibility security fix is not included;
  composition still requires verification. No dependency/journal format change,
  live GitHub/provider/device, user computer, credentials, Codex/extra paid
  execution, manual CI, merge or deployment. PowerShell/Windows/macOS untested.


## 2026-11-05 - SuperAgent optional PostgreSQL journal development slice

- Built M6b on #178 exact HEAD 6292543b / tree 3fd1dd4. New optional
  sharedpostgres implements the existing LeasedJournal without changing the
  scheduler, JSONL or SQLite APIs. The host supplies database/sql, TLS/credentials
  and one-time schema migration; the library opens no DSN or server.
- Post-lock clock_timestamp and final original-grant checks fence owner/epoch/seq
  in one transaction. An independent monotonic deadline cancels ownership despite
  blocked I/O. Unknown or late COMMIT receipts never update cache or retry work.
- bytea imports preserve raw header/LogItem bytes, including legacy batches,
  without repairing or writing the quiesced source. Only absent domains import.
- Offline test-first scripts cover lease timing, rollback, uncertainty, corruption,
  fencing, caller-owned DB and migration. Eight live PG tests are explicitly
  double-gated and were NOT run: no PostgreSQL service was available. Independent
  read-only review found no blocker and passed offline race x5 and vet.
- Real PostgreSQL, independent processes/hosts, TCP partitions and database restart
  remain unverified; keep the PR draft. See [M6b](superagent-m6b.md) and its package
  README. No local user machine, credentials, service, Codex/extra paid task,
  manual CI, merge or deployment; remote Linux Go 1.26.10 only.

## 2026-11-05 - SuperAgent durable local helper development slice

- M4c builds on #179 HEAD ec9bbe6b / exact tree c60213e4. The isolated local
  checkout used an equivalent-tree commit; publication is parented to the exact
  remote HEAD, with the final tested tree compared before branch creation.
- Added optional agent/background/helper: explicit fixed binary/tool registration,
  immutable operation/request/original epoch, durable admission/claim/stop/receipt,
  independent Linux helper processes, and query-only recovery. Existing Local,
  coordinator and device APIs remain unchanged; no dependencies added.
- Test-first missing API failure preceded implementation. Isolated self-written
  subprocesses prove coordinator death without relaunch, helper death remaining
  unknown, duplicate-claim prevention, stop and cancellation-ignoring late commit,
  old-epoch revocation and native coordinator review/completion integration.
- Independent review led to durable stop redelivery, fail-closed corrupt stops,
  terminal-before-stop-notification ordering, tombstone/quota and replay checks,
  escaped receipt size bounds and a bounded native admission handshake. Direct
  forged-record regression cases reject impossible admissions and transitions.
- Only durably observed facts are preserved: Local does not expose synchronous
  call receipts until return, so death before then remains unknown with quota.
  No PID/exit-code/no-effect inference, replay, rollback or detached-body guarantee.
- Full checks, repeated helper race, exact tree and independent review results are
  recorded in the draft PR. See [M4c](superagent-m4c.md). No production service,
  real device, user computer, credentials, live model, Codex/extra paid execution,
  manual CI, merge or deployment. Linux process-crash evidence is not host reboot,
  power-loss, adversarial filesystem or Windows/macOS acceptance.

## 2026-11-05 - SuperAgent GitHub HTTP protocol development slice

- M5b builds on #180 HEAD d4b50e45 / exact tree 3fe26ff3. The isolated remote
  checkout uses an equivalent-tree commit; publication uses the exact remote
  parent and compares the final tested tree before branch creation.
- Optional GitHubWebhook validates bounded raw-body HMAC, unambiguous supported
  events, exact repo/PR targets and independent host actor/intent authority.
  GitHub headers are unsigned: the durable event ID is a raw-body digest, and
  duplicates surface ErrGitHubReplay plus current evidence/original admission.
  Identical signed bodies cannot prove independent event identity or freshness;
  host audit and independent verification remain required. No automatic send.
- Optional GitHubTransport posts only through host client/endpoint/destinations,
  refuses redirects, validates exact 201 comment receipts, respects the later
  rate hint and never creates a token, retries POST or scans to prove absence.
  Unknown outcomes remain fenced across restart; no platform idempotency claim.
- Test-first missing-API failure, real httptest HTTP regressions and independent
  read-only review cover forged/raw-byte/header replays, cross-repo isolation,
  editing actor, grant drift, approved send/echo and lost-ack recovery. Review
  fixed dual-hint early retry and changed-grant replay receipt contracts.
- Full offline validation and exact final-tree evidence are in the draft PR.
  See [M5b](superagent-m5b.md). No real GitHub/GHES, existing credentials, user
  computer, live model, Codex/extra paid execution, manual CI, merge or deployment.


## 2026-11-05 - SuperAgent durable device chain integration slice

- M7b builds on draft #181 HEAD 998760a7 / exact tree 385244ee. It composes
  existing M6a sharedsqlite, M7a HTTP device and M4c helper implementations;
  production code, journal formats, module files and dependencies are unchanged.
- External-package fixtures run only our own endpoint/native test executables
  over loopback. Real endpoint process death leaves the helper alive; both
  registries reopen without dispatch, and recovery observes the original ID.
- Per-owner transports retain their original epoch. Explicit M6 Close/reopen
  takeover rejects all old commands before a replacement device command; native
  pre-effect permission remains bound to the original manifest epoch.
- Lost Start/Stop acknowledgments, faults before/after helper calls, native
  process failure, stop-before-start tombstones and late commits retain the
  existing unknown/review/quota barriers. Direct M7 inspection is read-only;
  the existing M4 replay watcher redelivers its persisted same-ID stop intent.
- Private result canary is produced in helper snapshots but absent from device
  HTTP/registry, coordinator state, Worker inputs, reports and replies. No raw
  result transport, credential-in-arguments guarantee or external exactly-once
  claim is added. Fixtures cover process crashes, not host reboot/power loss.
- Verification commands, exact final-tree evidence and independent review are
  recorded in the draft PR; see [M7b](superagent-m7b.md). No real device/model,
  production service, user computer, Codex/extra paid execution, CI trigger,
  merge or deployment. Real cross-host/TLS/production authority remain untested.


## 2026-11-05 - SuperAgent host-authorized Watchdog refresh slice

- M8b builds on draft #182 HEAD f13027d0 / exact tree d1a10893. Check/RunWatchdog
  remain read-only; a separate explicit host authorizer binds one current finding,
  task revision, original run/delivery and background operation to one Inspect.
- Durable reservation precedes the original backend's exact-ID read. Duplicate
  calls/restart return the same record without repeating Inspect; pending never
  auto-resumes. Atomic result + background facts preserve unknown/review/quota
  barriers and the existing task's single private completion on proven exit.
- New task input, newer finding, changed facts or old runtime invalidate unused
  decisions. Replay compares durable task facts and reconstructed running delivery,
  not process-local counters. SQLite takeover fences late authorization/results.
- Test-first API failure, offline boundary/replay/privacy/concurrency tests and
  external example cover the slice. Independent review identified incomplete
  replay freshness checks; fixed and re-reviewed with direct replay counterexamples
  plus legitimate active-Worker and terminal close/reopen coverage.
- Exact frozen-tree validation and publication evidence are in the draft PR; see
  [M8b](superagent-m8b.md). No Start/Stop/retry/Steer/new task or general repair API,
  real device/model/platform, user computer, Codex/extra paid execution, manual CI,
  merge or deployment. Existing background watchers retain their own contract.

## 2026-11-05 - GPT-6.1 Terra on the OpenAI Responses API

- Registered `openai/gpt-6.1-terra` with text and image input, a 1,050,000-token
  context window and 128,000 output tokens. Its default reasoning effort is
  medium. Explicit off sends `none` and enables temperature and top-p.
  [Model specification](https://developers.openai.com/api/docs/models/gpt-6.1-terra)
- Added standard and above-272K input pricing, including cached reads and
  30-minute writes. Long cache retention sends `prompt_cache_options.ttl`.
  [Pricing](https://developers.openai.com/api/docs/pricing) and
  [parameter guide](https://developers.openai.com/api/docs/guides/latest-model#gpt-6-astra-update-api-and-model-parameters)
- Windows Go 1.26.10 passed the full `pkg/pigo` tests and vet, plus focused
  race tests for Terra and sampling. A local HTTP fixture exercised function
  tools, reasoning, sampling and cache settings through `CompleteSimple`.


## 2026-11-05 - M9b real-runtime serial protocol slice

- Added `internal/runtimeeval` and `pi-superagent-runtime-eval`: actual
  PiMainRouter/PiWorker and single Runner + host subtask queue, four arms with
  identical 12-delivery inputs, one worker slot and request/call budget caps.
- Test-first event/effect/receipt/reply/history conservation; five repeat checks,
  source-scoped duplicates, callback fast paths, completed-journal reopen,
  controlled tool barriers, cancellation and uncooperative-body joining.
- Independent review corrected counter meaning (tasks, worker invocations,
  worker/outer tool calls and effects are separate) and result metadata checks.
- Scripted protocol evidence only, unknown token/cost/latency fields remain null.
  No historical baseline, real model performance, full M9/release acceptance,
  live services, user computer, additional paid work, CI, merge or deployment.
  See [M9b boundaries and reproduction](superagent-m9b.md).

## 2026-11-05 - M6c PostgreSQL coordinator acceptance preparation

- Added five double-gated `TestPostgresCoordinator*` entries on #185's exact
  baseline tree. Real OpenMainJournal plus in-process scripted callbacks covers
  migration boundaries, task/report/snapshot/subscription and outbox conservation,
  expired-lease takeover, unknown reply-claim COMMIT and background late evidence.
- Reused the existing disposable-database fixture, bounded operations and random
  domain cleanup. No runtime, dependency, schema or CI configuration changes.
- No PostgreSQL service was used: these cases compile and explicitly skip.
  Offline suite success is not real PostgreSQL coordinator acceptance. Restart
  means coordinator/journal reopen; ACK loss is injection, not a TCP partition.
- Full offline verification, independent review and exact-tree publication
  evidence are recorded in the draft PR. See [M6c](superagent-m6c.md).

## 2026-11-05 - M9c concurrent actual-runtime load conservation

- Added a separate 300-delivery/280-identity/20-task fixture through actual
  unbatched/batched subscribed SuperAgent and Pi adapters. Sixteen concurrent
  ingress goroutines, four gated native tool bodies, raw reports/receipts/replies
  and private histories are checked against post-execution expectations.
- Fixed virtual tool-gate service is explicitly separate from real runtime
  clocks. Variable batch grouping and script calls are retained as observations,
  not claimed as live-model performance, prompt-policy gains or historical data.
- Added test-first scorer corruption, malformed batch response, whole-run
  cancellation/join and completed-journal replay/conflicting-duplicate coverage.
- No production runtime, provider, dependency, schema or CI changes. Full offline
  checks, independent review and exact-tree publication are recorded in the
  draft PR. No final M9/release acceptance, real services, user computer,
  extra paid execution, CI dispatch, merge or deployment.
  See [M9c boundaries and reproduction](superagent-m9c.md).

## 2026-11-06 - M9d bounded mixed-runtime recovery acceptance

- Test-first follow-up to draft #187's exact tree, using actual unbatched/batched
  Pi main adapters and PiWorker. A separate 35-submission/23-identity/8-task
  fixture retains 30 terminal delivery records, including two-target fanout.
- A settles despite a blocked main model and B tool. Cancellation preserves A's
  late fsynced write and native committed receipt. Six independent requests
  complete while B remains running; the correction appears only in B's history.
- An isolated child exits without Close after A's committed-write/model failure
  and B's committed-write/pre-terminal boundary. Recovery preserves known facts,
  leaves B's unpersisted receipt unknown, blocks old follow-ups, and resumes only
  fresh instructions after fixture-host reconciliation. Exact before/after effect
  evidence and completed reopen reject repeats; corrupted evidence is rejected.
- Independent review tightened actual multi-input batch and exact reconciliation
  checks. Final full offline checks and exact-tree publication are recorded in
  the draft PR. No production API/dependency/schema/CI changes or live services.
  This is bounded protocol acceptance, not continuous-rate performance, full
  M9/four-strategy/release acceptance, or model token/cost evidence. No user
  computer, extra paid execution, CI dispatch, merge or deployment.
  See [M9d boundaries and reproduction](superagent-m9d.md).

## 2026-11-06 - SuperAgent PR merge review

- Reviewed #157–#161 and #163–#188 in dependency order. Joined the original
  #161 and #176 histories at their adapted implementations in #163 and #178.
  Preserved the separate edited-comment actor worklog entry from #176.
- Rejected hidden actions during failed main-input replay and fenced late
  background inspections after coordinator shutdown. The coordinator and durable
  helper now reject invalid arguments before authorization or quota reservation.
- Fixed drive-qualified SQLite filenames on Windows and blocked network/device
  paths. The device client now checks acceptance and native receipt evidence
  before trusting an active or terminal state.
- Repaired the late-commit PostgreSQL fixture to supply a native agent receipt.
  All eight journal and five coordinator cases passed on isolated PostgreSQL
  17.9 and 18.3 instances, both normally and under the Go race detector. These
  runs used one primary at a time and independent client pools.
- Added corrupt-evidence regressions for serial, concurrent, and crash
  evaluation. An empty effect, an extra reply to ignored input, or a reply
  tied to a different decision can no longer pass the corresponding scorer.
- The 256-event durable backlog exceeded the five-second small-fixture guard
  under concurrent test load on Windows. Its drain now has a separate
  one-minute deadline and still checks every terminal event and worker.
- Windows tests covered SQLite process contention and recovery. Linux helper
  and device process suites ran in WSL2. Provider calls, physical devices,
  cross-host failover, deployment, and release publication were outside scope.
