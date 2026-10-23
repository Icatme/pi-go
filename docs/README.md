# Documentation

Start with the [project overview and quick start](../README.md).

## Guides

| Guide | Covers |
| --- | --- |
| [Model calls and streaming](model-runtime.md) | Completion APIs, stream events, request options, and failure diagnostics |
| [Providers and authentication](providers.md) | Supported providers, credentials, routing, catalogs, and provider-specific behavior |
| [Account quota queries](account-quotas.md) | Account balances, usage windows, partial results, and CLI output |
| [Development](development.md) | Builds, module checks, tests, coverage, and live-test controls |
| [Release notes](releases/) | Changes and upgrade notes by version |

## Agent runtime

| Reference | Covers |
| --- | --- |
| [Agent guide](../agent/README.md) | Direct usage, runners, snapshots, tool execution, and prebuilt helpers |
| [Runtime contracts](../agent/docs/runtime-contracts.md) | Runtime state, lifecycle events, and integration guarantees |
| [Agent testing](../agent/docs/testing.md) | Offline coverage, race checks, and live provider tests |
| [Sessions](../agent/session/README.md) | Storage, branching, replay, compaction, and interrupted execution |
| [Checkpoints](../agent/checkpoint/) | Revisioned checkpoints and targeted tool approval |
| [Turn loop](../agent/turnloop/) | Bounded input admission across sequential runner invocations |
| [Skills](../agent/skills/README.md) | Trusted disk/embedded catalogs, metadata, and resource reads |
| [Strict schemas](../agent/strictschema/README.md) | Required/nullable declarations and optional-null normalization |

## MCP and JavaScript tools

- [MCP connections](../mcp/README.md) — managed HTTP/stdio sessions, catalogs, and OAuth
- [MCP tools](../agent/mcptools/README.md) — bind MCP tools to agent execution
- [MCP resources](../agent/mcpresources/README.md) — bounded reads and explicit artifact export
- [Dynamic toolsets](../agent/toolset/README.md) — exposure, discovery, and trusted restoration
- [Codemode](../codemode/README.md) — bounded QuickJS WASM sandbox and invocation-local storage
- [Agent Codemode binding](../agent/codemodetool/README.md) — explicit tool allowlists and execution contracts

Runnable integrations are listed in [Examples](../README.md#examples). The local
MCP Codemode example uses a caller-owned SDK session and a deterministic model;
the managed example adds JSONL branch transactions, compaction, and process resume.

## Design and repository notes

- [Native custom tool inputs](native-tools.md)
- [Native dynamic tool declarations](dynamic-tool-anchors.md)
- [Codemode design](design-codemode-wazero.md)
- [Pi 1.0 scope and follow-up](pi-1-followup.md)
- [Contributor and agent working rules](../AGENTS.md)
- [Exported provider package](../pkg/pigo/)
