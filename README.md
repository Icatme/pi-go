# pi-go

[![Go Reference](https://pkg.go.dev/badge/github.com/Icatme/pi-go.svg)](https://pkg.go.dev/github.com/Icatme/pi-go)
[![Go version](https://img.shields.io/github/go-mod/go-version/Icatme/pi-go)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

Go libraries and a CLI for calling language models and building single-agent applications. `pi-go` reimplements the core semantics of [Pi AI](https://github.com/earendil-works/pi/tree/main/packages/ai), with a shared model/message API, streaming, tool calls, reasoning content, and cross-provider replay.

The provider layer (`pkg/pigo`) and agent runtime (`agent`) are separate. MCP connections, JavaScript tools, persistent sessions, and skill catalogs are optional packages.

[Install](#install) · [Quick start](#quick-start) · [CLI](#cli) · [Packages](#packages) · [Examples](#examples) · [Documentation](docs/README.md)

## Install

Requires **Go 1.26.2 or newer**.

Add the library to an existing Go module:

```powershell
go get github.com/Icatme/pi-go@latest
```

Install the CLI:

```powershell
go install github.com/Icatme/pi-go/cmd/pigo@latest
pigo --help
```

`agent` and the optional packages ship in the same module; no separate installation is needed.

## Quick start

Set a provider credential, then save the following as `main.go` in your Go module:

```powershell
$env:KIMI_API_KEY = "your-api-key"
```

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/Icatme/pi-go/pkg/pigo"
)

func main() {
	apiKey := os.Getenv("KIMI_API_KEY")
	if apiKey == "" {
		log.Fatal("set KIMI_API_KEY")
	}
	model := pigo.GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		log.Fatal("model not found")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	result := pigo.CompleteSimple(*model, pigo.Context{
		SystemPrompt: "You are a concise coding assistant.",
		Messages: []pigo.Message{
			pigo.UserMessage{Content: "Explain Go interfaces in three sentences."},
		},
	}, pigo.SimpleStreamOptions{
		APIKey:         apiKey,
		RequestContext: ctx,
	})
	if result.StopReason == pigo.StopReasonError || result.StopReason == pigo.StopReasonAborted {
		log.Fatal(result.ErrorMessage)
	}
	for _, block := range result.Content {
		if text, ok := block.(pigo.TextContent); ok {
			fmt.Println(text.Text)
		}
	}
}
```

Build and run it from PowerShell:

```powershell
go build -o hello-pigo.exe .
.\hello-pigo.exe
```

For incremental output, use `StreamSimple`. See [model calls and streaming](docs/model-runtime.md) for event handling, sampling options, and error diagnostics. To add a tool-execution loop, start with the [agent guide](agent/README.md).

## CLI

The CLI supports login, model discovery, prompts, and account quota queries:

```powershell
pigo login openai-codex
pigo models openai-codex
pigo ask --provider openai-codex --model gpt-5.5 "Explain this error message"

pigo login commandcode
pigo models commandcode
pigo quota commandcode
pigo quota --json commandcode
```

`pigo list` lists available login providers. CLI credentials are stored in `.pigo/auth.json`; library applications supply their own credentials through request options or auth configuration.

Primary providers are `openai-codex`, `opencode-go`, `kimi-coding`, `commandcode`, `anthropic`, `deepseek`, `google`, and `mistral`. See [providers and authentication](docs/providers.md) for routing, credential lookup, model discovery, and provider-specific behavior. See [account quota queries](docs/account-quotas.md) for balance and usage-window semantics.

## Packages

| Package | Use it for |
| --- | --- |
| [`pkg/pigo`](pkg/pigo) | Model metadata, provider protocols, completions, streams, and message replay |
| [`agent`](agent/README.md) | Single-agent loops, tool execution, steering, follow-ups, and snapshots |
| [`agent/session`](agent/session/README.md) | Persistent conversation branches, replay, and compaction |
| [`mcp`](mcp/README.md) | Managed HTTP/stdio MCP connections, catalogs, and explicit OAuth |
| [`agent/toolset`](agent/toolset/README.md) | Dynamic tool exposure, discovery, and trusted session restoration |
| [`codemode`](codemode/README.md) / [`agent/codemodetool`](agent/codemodetool/README.md) | Bounded JavaScript execution with QuickJS WASM and an explicit tool allowlist |
| [`agent/skills`](agent/skills/README.md) | Trusted directory or embedded skill catalogs and bounded resource reads |
| [`agent/strictschema`](agent/strictschema/README.md) | Required/nullable tool declarations and optional-null normalization |

`agent` depends on `pkg/pigo`; provider protocol code stays outside the agent loop. Applications own graph and multi-agent orchestration. The [documentation index](docs/README.md) also covers checkpoints, runtime contracts, MCP tools, and resources.

## Examples

Examples live in a separate Go module under `examples/`. Each guide includes its build and run commands.

| Example | Demonstrates |
| --- | --- |
| [switch-chat](examples/switch-chat/README.md) | Agent presets, session persistence, and reflection |
| [mattermost-chatbot](examples/mattermost-chatbot/README.md) | A Mattermost application with built-in tools |
| [mcp-codemode](examples/mcp-codemode/README.md) | A caller-owned MCP session and deterministic model using bounded JavaScript tools |
| [mcp-managed](examples/mcp-managed/README.md) | Managed MCP connections, JSONL sessions, compaction, and process restart |

## Development

From the repository root:

```powershell
$env:GOWORK = "off"
$env:PIGO_LIVE_TEST = "0"
$env:PI_GO_AGENT_LIVE_TEST = "0"
$env:PI_GO_AGENT_EXAMPLE_LIVE = "0"
.\scripts\test.ps1
.\scripts\build.ps1 -SkipTest
```

The test script checks the root and examples modules independently. The build script creates `bin/pigo.exe` on Windows. See the [development guide](docs/development.md) for focused tests, race checks, coverage, cross-builds, and live-test controls.

## Documentation

- [Documentation index](docs/README.md) — guides and package references
- [Model calls and streaming](docs/model-runtime.md) — completion APIs, events, options, and diagnostics
- [Providers and authentication](docs/providers.md) — credentials, routing, and catalog behavior
- [Account quota queries](docs/account-quotas.md) — balances, usage windows, and CLI output
- [Release notes](docs/releases/) · [Source tags](https://github.com/Icatme/pi-go/tags) · [GitHub releases](https://github.com/Icatme/pi-go/releases)

## License

[MIT](LICENSE). The license retains the copyright notice for the original Pi AI
code by Mario Zechner. Bundled QuickJS components retain their
[upstream license notices](codemode/internal/assets/SOURCE.md).
