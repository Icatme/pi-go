# Agent package guidelines

## Scope

- This package is the Go port of `pi-agent-core`.
- Keep the focus on single-agent runtime behavior.
- Do not turn this package into a graph or multi-agent runtime.

## Architecture boundaries

- `agent` owns the agent loop, message model, tool lifecycle, and runtime state.
- `StreamModel` is the primary model boundary; provider-specific transport stays outside the loop.
- The built-in provider implementation may depend on `pkg/pigo`; `pkg/pigo` must not depend on `agent`.
- Graph and multi-agent orchestration must remain outside the core runtime.

## Working rules

- Find the root cause before changing behavior and add regression coverage when possible.
- Prefer direct changes over compatibility wrappers.
- Keep exported APIs intentional and keep documentation aligned with the actual code.
- Preserve the original `pi-agent-core` prompt, continue, steer, follow-up, message, and tool semantics.
