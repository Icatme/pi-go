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
- A stateless `Runner` for asynchronous, snapshot-in/snapshot-out execution
- An optional `agent/session` package for append-only lane logs, repositories,
  pure replay, and turn-safe compaction
- A native `prebuilt.PiAgent` direct re-export of `agent.Agent`
- A native `prebuilt.CreateAgent(...)` helper for the common model-plus-tools constructor path
- A native `prebuilt.ChatAgent` session wrapper built on the same runtime
- A task-only `prebuilt.AgentTool` that runs one isolated child `Runner`
- A structured `prebuilt.ReflectionAgent` with typed accept/revise evaluations
- Package-level loop façades: `RunAgentLoop`, `RunAgentLoopContinue`

## Package Layout

- `agent/`
  - core runtime types, state, engine, and agent wrapper
- `agent/prebuilt`
  - native high-level wrappers implemented on top of the core runtime
  - task-scoped child agents and structured reflection evaluators
  - does not reintroduce graph orchestration into the root module
- `agent/session`
  - provider-independent durable conversation primitives
  - memory and versioned JSONL repositories
  - pure lane/context replay and pluggable compaction summaries

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

For application boundaries that should not retain mutable agent state, use a
`Runner`. Each invocation owns a run id and monotonically sequenced event
stream, and returns a new snapshot without mutating the caller's input. A
`Runner` started from another run's context automatically records the outer
run as its `ParentRunID`:

```go
runner, err := core.NewRunner(core.AgentDefinition{
	Name:  "assistant",
	Model: echoModel{},
})
if err != nil {
	panic(err)
}

run := runner.Query(context.Background(), "Hello")
for event := range run.Events() {
	fmt.Println(event.RunID, event.Sequence, event.Type)
}
snapshot, err := run.Wait()
_ = snapshot
_ = err
```

Runner events are lossless and use a bounded buffer. Drain `Events` before
calling `Wait`; call `Close` to cancel and drain a run that is being abandoned.

## Agents As Task Tools

`prebuilt.NewAgentTool` exposes one named definition as a strict
`{"task":"..."}` tool. The child receives only that task in a fresh snapshot;
its internal events are forwarded as transient tool updates and its final
output becomes one ordinary parent tool result:

```go
delegate, err := prebuilt.NewAgentTool(prebuilt.AgentToolConfig{
	Definition: core.AgentDefinition{
		Name:  "researcher",
		Model: echoModel{},
	},
	Description: "Research one isolated question.",
	Limits: prebuilt.AgentToolLimits{
		MaxDepth: 1,
		MaxTurns: 4,
		Timeout:  30 * time.Second,
	},
})
```

Depth limits are absolute across nested task tools and cannot be widened by a
child. `MaxTurns` is a per-child upper bound. `Timeout` derives a cooperative
child cancellation deadline; the earlier parent deadline still wins, and child
models, tools, and streams must honor context cancellation. It is not a hard
wall-clock bound over implementations that ignore context. A child's
termination hint never terminates its parent.

Reflection uses an exact `accept` / `revise` verdict rather than searching
critic prose for keywords. Every generated draft, including the last allowed
draft, has a matching evaluation. The built-in model evaluator requires one
strict JSON object; applications may inject a typed `ReflectionEvaluator`
instead.

## Durable Sessions

The optional `agent/session` child package keeps persistence out of the core
loop while providing a shared memory/JSONL contract:

```go
repo, err := sessionstore.NewJSONLRepository("./sessions")
if err != nil {
	panic(err)
}
durable, err := repo.Create(sessionstore.Header{ID: "chat-1"}, sessionstore.Options{})
if err != nil {
	panic(err)
}
defer durable.Close()

_, _ = durable.AppendMessage(sessionstore.MainLane, core.NewUserTextMessage("Hello"))
context, _ := durable.Context(sessionstore.MainLane)
_ = context.Messages
```

Import the child package as:

```go
import sessionstore "github.com/Icatme/pi-go/agent/session"
```

Compaction keeps its summary separate from runtime messages. Applications must
explicitly project that summary into their model context; the session package
does not disguise it as a user instruction.

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

Durable transcript storage and safe compaction live in `agent/session`; they do
not add orchestration behavior to the root `agent` package.

Task-scoped child execution lives in `agent/prebuilt`; it composes independent
single-agent runners and does not add routing or graph state to the core.

## Current Limitations

- Multi-node orchestration, supervisor-style routing, checkpointing, time travel,
  and human-in-the-loop workflows are intentionally outside the core runtime.
