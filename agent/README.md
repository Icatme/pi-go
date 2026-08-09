# pi-go/agent

`agent` is a standalone single-agent runtime. It is intentionally separate from
`prebuilt/` so the core runtime and higher-level helpers keep clear boundaries.

## Project Goals

Core goals:

- Port the original `pi-agent-core` single-agent runtime into idiomatic Go.
- Preserve the original runtime behavior where it matters:
  - prompt / continue / steer / follow-up
  - assistant message lifecycle
  - tool execution lifecycle
  - runtime state and event flow
- Keep `StreamModel` as the stable backend boundary so providers and gateways can
  be integrated without pushing transport logic into the core runtime.
- Use `pi-go` as the built-in default provider implementation when a
  `ModelRef{Provider, Model}` is configured.
- Keep the package usable on its own and expose a stable boundary for external
  orchestration packages.

Non-goals:

- This package does not aim to own multi-agent, supervisor, planner, or
  tree-of-thoughts workflows in the core package.

Success means:

- the core package can run independently in Go
- the core package stays aligned with the original `pi-agent-core` semantics
- backend integrations happen behind `StreamModel`, not inside the agent loop

## What It Provides

- A serializable `AgentSnapshot`
- A user-facing `AgentOptions` / `InitialState`
- A low-level `AgentDefinition` / `DefinitionResolver`
- A built-in default `StreamModel` backed by `pi-go` provider implementations
- A turn-based `Engine` with:
  - assistant message streaming
  - tool execution
  - JSON Schema argument validation
  - `beforeToolCall` / `afterToolCall`
  - per-tool sequential execution and batch termination hints
  - `prepareNextTurn` / `shouldStopAfterTurn`
  - `steer` / `followUp`
  - `continue`
- A higher-level `Agent` wrapper
- A native `prebuilt.PiAgent` direct re-export of `agent.Agent`
- A native `prebuilt.CreateAgent(...)` helper for the common model-plus-tools constructor path
- A native `prebuilt.ChatAgent` session wrapper built on the same runtime
- A native `prebuilt.ReflectionAgent` helper for sequential draft-and-reflect passes
- Package-level loop façades: `RunAgentLoop`, `RunAgentLoopContinue`

## Package Layout

- `agent/`
  - core runtime types, state, engine, and agent wrapper
- `agent/prebuilt`
  - native high-level wrappers implemented on top of the core runtime
  - does not reintroduce graph orchestration into the root module

## Direct Usage

```go
package main

import (
	"context"
	"fmt"

	core "github.com/Icatme/pi-go/agent"
)

type echoModel struct{}

func (echoModel) Stream(ctx context.Context, request core.ModelRequest) (core.AssistantStream, error) {
	panic("implement StreamModel with your provider client")
}

func main() {
	agent, _ := core.NewAgentWithOptions(core.AgentOptions{
		Model: echoModel{},
		InitialState: core.AgentInitialState{
			SystemPrompt: "You are a helpful assistant.",
		},
	})

	agent.Subscribe(func(event core.AgentEvent) {
		if event.Type == core.EventMessageUpdate {
			fmt.Print(event.Delta)
		}
	})

	_ = agent.PromptText(context.Background(), "Hello")
}
```

`StreamModel` is the primary model abstraction. Integrate providers by
implementing that interface directly, or use `StreamFunc` when a function-style
adapter is enough.

If you only need built-in provider execution, set `InitialState.ModelRef` or
`AgentDefinition.DefaultModel` with `Provider` and `Model`. The runtime will
resolve the default `pi-go` provider implementation automatically.

Formal runtime semantics live in [`docs/runtime-contracts.md`](docs/runtime-contracts.md).
Testing entrypoints and release checks live in [`docs/testing.md`](docs/testing.md).

`ModelRef.ProviderConfig` carries typed provider runtime settings when needed:

- `base_url`: override the provider base URL
- `api_key`: explicit API key or bearer token
- `headers`: extra request headers as `map[string]string`
- `auth`: typed provider auth config for provider-specific auth flows

User image input now follows the same shape as `pi-agent-core` and `pi-go`:

- `NewImagePart(data, mimeType)` expects base64 image data, not a remote URL
- user messages can mix text and image parts in one message
- the built-in `pi-go` provider path forwards those image parts directly to the
  selected provider

The current stable core runtime surface is:

- `Agent`
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

This package does not keep a compatibility shim for legacy image URLs.

Live provider tests are manual release checks, not default CI. See
[`docs/testing.md`](docs/testing.md) for the exact environment variables, skip
behavior, and commands.

## Current Scope

This package is the single-agent runtime core, not a graph orchestration runtime.

Use `agent` for:

- single-agent runtime behavior
- message lifecycle
- tool lifecycle
- assistant messages that preserve `response_id`, `provider`, `api`, `model`,
  thinking signatures, and original tool call ids
- user-facing agent construction via `AgentOptions`
- dynamic low-level agent definition when needed

The default model transport is `TransportAuto`. Callers can still select SSE,
WebSocket, or cached WebSocket explicitly. `ThinkingMax` is forwarded through
the provider layer and clamped to a model-supported level when necessary.

Keep these concerns in outer orchestration packages:

- multi-agent orchestration
- conditional routing
- checkpointing / time travel / HITL
- graph composition

## Current Limitations

- Multi-node orchestration, supervisor-style routing, checkpointing, time travel,
  and human-in-the-loop workflows are intentionally outside this package.
