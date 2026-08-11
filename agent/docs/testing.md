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
  backpressure, and concurrent `Wait` / `Close`
- shared memory/JSONL session storage conformance, strict replay, torn-tail
  repair, repository writer claims, and path isolation
- pure lane/context reduction plus complete-turn, tool-pair-safe compaction

## Race Checks

Recommended pre-release verification:

```powershell
go test ./... -race
```

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
- `session`

Non-goals for this test plan:

- no compatibility shim for legacy image URLs
- no provider matrix beyond providers supported by the built-in `pi-go` path
