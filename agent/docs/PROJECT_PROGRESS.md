# Project Progress

## Project Goals

### Core Goal

Build `pi-go/agent` as a Go port of the original `pi-agent-core`,
with focus on the single-agent runtime rather than graph orchestration.

### In Scope

- Port the original single-agent runtime semantics into Go.
- Keep the prompt / continue / steer / follow-up model aligned with the source project.
- Preserve tool execution flow, runtime state, and event contracts as closely as practical.
- Provide a stable backend boundary through `StreamModel`.

### Out of Scope

- Building a graph orchestration runtime.
- Adding multi-agent, supervisor, planner, or tree-of-thoughts logic into the core package.
- Building provider-specific behavior directly into the core runtime.
- Carrying compatibility layers that do not solve a current, concrete need.

### Success Criteria

- `pi-go/agent` can be used as an independent Go package.
- Core runtime behavior stays close to the original `pi-agent-core`.
- Backend integrations remain behind `StreamModel`.

## Current Status

The runtime is maintained in the `pi-go` repository as a package with a one-way dependency on `pkg/pigo`.

- Repository: `github.com/Icatme/pi-go`
- Import path: `github.com/Icatme/pi-go/agent`
- Local verification: `go test ./agent/...`

## Completed Work

### Core Runtime

- Implemented the core agent runtime with prompt, continue, steer, follow-up, abort, and idle waiting behavior.
- Implemented runtime state tracking, including streaming status, pending tool calls, and error state.
- Extended pending tool-call tracking so runtime state can retain original provider tool ids alongside normalized ids.
- Implemented tool execution flow with before/after hooks and sequential or parallel execution modes.
- Implemented message conversion and context transformation boundaries.
- Expanded the message model to preserve provider/runtime fields such as `response_id`, `provider`, `api`, `model`, thinking signatures, and original tool call ids.

### Public API

- Added `AgentOptions` and initial-state oriented construction.
- Added package-level loop helpers mirroring the original runtime shape.
- Added a native `prebuilt.PiAgent` direct exposure of the core `Agent`,
  without rebuilding graph state or maintaining a second wrapper runtime.
- Added a native `prebuilt.CreateAgent(...)` helper that turns the common
  model-plus-tools setup into a direct `agent.Agent` constructor path.
- Added a native `prebuilt.ChatAgent` wrapper for session-oriented single-agent
  chat with runtime-backed streaming and dynamic tool management.
- Added a native `prebuilt.ReflectionAgent` helper that runs sequential
  draft-and-reflect passes on top of one-shot `agent.Agent` executions,
  instead of introducing graph orchestration into the root module.
- Added prompt convenience methods for text and image input, with image parts now aligned to the original `pi-agent-core` / `pi-go` base64-plus-MIME shape.
- Added custom message helpers without copying TypeScript-only declaration-merging patterns.
- Added built-in default provider resolution through `pi-go` when a `ModelRef{Provider, Model}` is configured.
- Added typed `ProviderConfig` / auth config on `ModelRef` so default provider execution no longer depends on ad-hoc metadata keys.

### Event Model

- Added assistant event types for start, text lifecycle, tool-call lifecycle, done, and error.
- Tightened runtime tests around streaming state, abort behavior, turn events, and tool-execution events.
- Verified the default `pi-go` provider path preserves reasoning deltas, tool-call deltas, replay signatures, raw tool ids, and provider response ids.
- Added offline provider regression coverage for provider error mapping, codex failure metadata, mixed history replay, and tool-result image replay.
- Added concurrent request and subscriber-state stress coverage for burst prompt/continue/update paths.

## Current Boundaries

- Core focus remains the original `pi-agent-core` runtime.
- Multi-agent, supervisor, planner, and graph-native orchestration are out of scope for core.
- `StreamModel` is the main long-term interface for model backends, with `pi-go` as the built-in default provider implementation.

## Known Design Notes

- Provider-specific streaming fidelity now flows through the built-in `pi-go` path for supported providers, while custom `StreamModel` implementations can still define their own fidelity.
- Formal runtime semantics are now documented in `docs/runtime-contracts.md`.
- Default offline and gated live test entrypoints are now documented in `docs/testing.md`.

## Recommended Next Steps

1. Continue checking remaining differences against the original `pi-agent-core` tests and contracts.
2. Keep future backend integrations behind `StreamModel` instead of pushing provider details into the core runtime.
3. Treat `docs/runtime-contracts.md` and `docs/testing.md` as the source of truth when changing runtime semantics or release checks.
