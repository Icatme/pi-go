# Testing

The agent runtime uses two test layers:

- default offline tests, which must pass in normal development and CI
- gated live provider tests, which are intended for release checks and manual verification

## Default Offline Coverage

Run from the repository root:

```powershell
go test ./agent/...
```

The default offline suite covers:

- core prompt / continue / steer / follow-up behavior
- tool execution lifecycle, including `beforeToolCall` / `afterToolCall`
- schema validation before and after tool-hook mutation, truncated tool-call
  rejection, per-tool execution mode, termination hints, and late-update gating
- active-reset rejection and turn-boundary hook ordering
- mixed replay invariants for provider metadata, reasoning signatures, images,
  raw tool ids, and tool results
- provider error mapping on the built-in `pi-go` path
- snapshot JSON round-trip for runtime state
- concurrent request rejection and state consistency under burst message updates
- flat assistant-update event fields and balanced stream-close error lifecycle
- stateless runner snapshot isolation, run metadata, bounded lossless
  backpressure, nested parent lineage, and concurrent `Wait` / `Close`
- bounded external-input admission, per-class FIFO and queue-mode batching,
  next-run isolation, cross-run snapshot continuity, MaxTurns handoff safety,
  synchronous event backpressure, graceful drain, cooperative immediate stop,
  push/stop races, and detached concurrent TurnLoop waits
- whole-batch tool-gate fail-closed behavior, canonical pending bindings,
  lossless JSON-number/raw-presence round trips, lifecycle-free suspension,
  exact resume validation, pre-execution retry, MaxTurns continuity, and resume
  turn-hook ordering
- targeted checkpoint approval, partial/stale decision handling, capability ID
  rotation and consumption, argument-drift re-interruption, strict envelopes,
  recoverable pre-commit errors, CAS concurrency, indeterminate terminal writes,
  and stream ownership
- task-only AgentTool input, transient child events, depth/turn/time budgets,
  child termination isolation, error draining, and final-result persistence
- strict structured reflection verdicts, complete generation/evaluation pairs,
  ownership isolation, cancellation, and terminal-output rejection
- shared memory/JSONL session storage conformance, strict replay, torn-tail
  repair, repository writer claims, and path isolation
- pure lane/context reduction plus complete-turn, tool-pair-safe compaction
- original tool error chains, independent execution facts, safe after-hook
  projection, complete child ledgers, queued permission revocation, FIFO
  admission and permits held until actual executor exit
- fixed QuickJS WASM stack/heap/deadline enforcement, bounded bridge/output
  resources, cancellation/Close ownership, unsafe numbers, and invocation-store
  success-only atomic commits
- real SDK in-memory MCP and deterministic model integration through
  `agent/codemodetool`, including business-error envelopes and nested Suspend

## Race Checks

Recommended pre-release verification:

```powershell
go test ./... -race
```

For the sandbox/MCP contract, run from the repository root with live calls off:

```powershell
$env:GOWORK = 'off'
$env:PIGO_LIVE_TEST = '0'
$env:PI_GO_AGENT_LIVE_TEST = '0'
go test -mod=readonly -count=1 -race ./codemode/... ./agent ./agent/mcptools ./agent/codemodetool
```

The GitHub Actions matrix runs this contract and the compiled local MCP example
on Windows amd64, Linux amd64/arm64, and macOS amd64/arm64. Cross-compilation
alone is not sandbox execution evidence. The local example needs no credentials
or UI acceptance; real provider tests remain separately gated.

## Live Provider Tests

Live tests are gated and are not part of the default offline suite.

Enable them with:

```powershell
$env:PI_GO_AGENT_LIVE_TEST = "1"
go test ./... -run Live
```

Supported live providers:

- `anthropic / claude-sonnet-4-5`
- `kimi-coding / k2p5`
- `openai-codex / gpt-5.4`

Credentials are read from:

- Anthropic: `ANTHROPIC_API_KEY`
- Kimi: `KIMI_API_KEY`
- OpenAI Codex: `PI_GO_AGENT_OPENAI_CODEX_TOKEN` or `OPENAI_CODEX_TOKEN`

Expected behavior:

- If `PI_GO_AGENT_LIVE_TEST != 1`, live tests skip.
- If a provider credential is missing, only that provider case skips.
- Live tests are intended to validate:
  - basic prompt
  - multi-turn context retention
  - tool execution loop
  - abort after first streamed delta
  - continue from a user tail
  - continue from a tool-result tail

## Stable API Surface

The current stable core runtime surface is:

- `Agent`
- `Runner`
- `Engine`
- `AgentDefinition`
- `ModelRef`
- `ProviderConfig`
- `Message`
- `ToolCall`
- `ToolResultPayload`
- `StreamModel`

Secondary integration surfaces:

- `prebuilt`
- `checkpoint`
- `session`
- `turnloop`

Non-goals for this test plan:

- no compatibility shim for legacy image URLs
- no provider matrix beyond providers supported by the built-in `pi-go` path
