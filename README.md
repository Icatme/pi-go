# pi-go

[![Go Reference](https://pkg.go.dev/badge/github.com/Icatme/pi-go.svg)](https://pkg.go.dev/github.com/Icatme/pi-go)
[![GitHub release](https://img.shields.io/github/v/release/Icatme/pi-go)](https://github.com/Icatme/pi-go/releases)
[![Go version](https://img.shields.io/github/go-mod/go-version/Icatme/pi-go)](go.mod)

`pi-go` is a Go reimplementation of the [`pi.ai`](https://github.com/badlogic/pi-mono/tree/main/packages/ai) core package surface. It combines a provider-agnostic model runtime in `pkg/pigo`, a single-agent runtime in `agent`, and the `pigo` CLI while keeping provider protocol code out of the agent loop.

## Highlights

- One model and message surface across OpenAI Codex, Kimi Coding, Command Code, Anthropic, DeepSeek, Google, and Mistral
- Blocking and streaming completion APIs with normalized cross-provider message replay
- SSE and WebSocket transports, tool calls, reasoning content, usage accounting, and observer hooks
- A separate single-agent runtime with message, tool, steering, follow-up, and snapshot lifecycles
- A small CLI for provider login, model discovery, and prompts

## Project Positioning

The low-level `pkg/pigo` package is not an agent framework. It provides the model and provider pieces needed to:

- describe models and provider capabilities
- route requests through provider and API registries
- execute `Complete`, `CompleteSimple`, `Stream`, and `StreamSimple`
- normalize message history for cross-provider replay
- expose incremental assistant events and final responses
- validate tool arguments with a small built-in schema subset

The optional `agent` package builds a single-agent loop, tool lifecycle, snapshots,
steering, and follow-up behavior on that provider layer. It deliberately does not
own graph or multi-agent orchestration.

## Supported Providers

Current primary provider scope:

- `openai-codex` via OpenAI OAuth and the Responses-style API surface
- `kimi-coding` via Anthropic-style Messages semantics
- `commandcode` via the `commandcode-custom` streaming protocol used by `pi-commandcode-provider`
- `anthropic` via the Anthropic Messages API
- `deepseek` via DeepSeek chat completions
- `google` via the Gemini Generative AI API
- `mistral` via the Mistral chat completions API

Supporting provider and API modules also exist for shared protocol behavior used by the core library, but the repo is intentionally optimized around these providers.

## Install

The repository currently requires Go 1.26.2 or newer.

Add the core library to a Go module:

```powershell
go get github.com/Icatme/pi-go@latest
```

Install the CLI:

```powershell
go install github.com/Icatme/pi-go/cmd/pigo@latest
pigo --help
```

The `agent` package was added after the current `v0.4.1` release and is available from `master`:

```powershell
go get github.com/Icatme/pi-go/agent@master
```

Import it with:

```go
import "github.com/Icatme/pi-go/agent"
```

## Quick Start

Core library import path:

```go
import "github.com/Icatme/pi-go/pkg/pigo"
```

Basic request flow:

```go
package main

import "github.com/Icatme/pi-go/pkg/pigo"

func main() {
	model := pigo.GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		panic("model not found")
	}

	result := pigo.CompleteSimple(*model, pigo.Context{
		SystemPrompt: "You are a precise coding assistant.",
		Messages: []pigo.Message{
			pigo.UserMessage{Content: "Explain what this repository does."},
		},
	}, pigo.SimpleStreamOptions{
		APIKey: "kimi-api-key",
	})

	_ = result
}
```

Streaming flow:

```go
stream := pigo.StreamSimple(*model, pigo.Context{
	Messages: []pigo.Message{pigo.UserMessage{Content: "hello"}},
}, pigo.SimpleStreamOptions{APIKey: "kimi-api-key"})

for event := range stream.Events() {
	_ = event
}

final := stream.Result()
_ = final
```

## Architecture Overview

| Path | Responsibility |
| --- | --- |
| [`pkg/pigo`](pkg/pigo) | Exported model, provider, protocol, streaming, and tool library |
| [`agent`](agent) | Exported single-agent runtime and `prebuilt` helpers |
| [`examples`](examples) | Runnable examples in a dependency-isolated nested module |
| [`cmd/pigo`](cmd/pigo) | CLI entrypoint |
| [`internal/cli`](internal/cli) | CLI-only login and credential-store logic |
| [`docs`](docs) | Repository notes and supporting documentation |

Core runtime pieces:

- lazy provider registry for provider modules and model metadata
- lazy API registry for stream dispatch by `model.API`
- unified message/content model for replay and serialization
- bounded stream event delivery with explicit droppable delta backpressure
- shared HTTP/SSE transport utilities for OpenAI Responses-style providers
- optional WebSocket transport for `openai-codex`
- observer hooks for request completion/error and stream-finish accounting

## Testing

Main package validation commands:

```powershell
go test ./pkg/pigo/... -v -count=1
.\scripts\test.ps1 -Coverage
go vet ./pkg/pigo/...
gofmt -l pkg/pigo/
```

Run all repository modules through the shared PowerShell entrypoint:

```powershell
.\scripts\test.ps1
```

Direct package checks for the merged agent runtime remain available with
`go test ./agent/...`; the shared script tests the root and examples modules
independently.

For direct coverage commands, prefer a forced rebuild after package file moves or splits so the profile does not reuse stale cover metadata from the Go build cache:

```powershell
go test -a ./pkg/pigo/... -coverprofile=coverage.out -count=1
go tool cover -func coverage.out
```

Race testing:

```powershell
go test ./pkg/pigo/... -race -count=1
```

Live tests are gated and skipped by default. Enable them explicitly with:

```powershell
$env:PIGO_LIVE_TEST = "1"
```

Some live paths also require credentials such as:

- `KIMI_API_KEY`
- `COMMANDCODE_API_KEY`
- test-only OpenAI Codex credentials in `01_auth.json`

Support-file boundaries:

- `.pigo/auth.json` is CLI-managed auth storage
- `.pigo/.env` is local test/helper API-key input
- `01_auth.json` is test-only and ignored by git
- library runtime auth should normally be passed explicitly via options or auth config; `commandcode` additionally supports the upstream user-home auth files listed below

The `commandcode` provider matches `pi-commandcode-provider` v0.4.3's setup
surface. `pigo login commandcode` opens the browser-assisted login flow and
stores the returned API key in `.pigo/auth.json`. Runtime lookup also accepts
`COMMANDCODE_API_KEY`, `SimpleStreamOptions.APIKey`, caller-supplied
`AuthConfig`, and the supported Command Code/pi/OMP auth-file shapes under the
user home directory: `.commandcode/auth.json`, `.omp/agent/auth.json`, and
`.pi/agent/auth.json`.

The CLI refreshes Command Code models from
`https://api.commandcode.ai/provider/v1/models` before `models` and `ask`, then
atomically caches the validated catalog in the Pi-compatible
`~/.pi/agent/commandcode-models.json` format. `PI_CODING_AGENT_DIR` changes the
agent directory and `COMMANDCODE_MODELS_CACHE` overrides the complete cache
path. If live discovery is unavailable, a valid cache remains usable with a
warning; the first offline load without a valid cache leaves Command Code
unavailable without preventing other providers from loading.

Library callers can use `FetchCommandCodeModels` for a strict read-only live
catalog, `LoadCommandCodeModels` for the live/cache selection, or
`RefreshCommandCodeModelsWithResult` to apply that selection while retaining
its source and warning. `RefreshCommandCodeModels` remains the concise wrapper
for callers that only need the applied model slice. Override the endpoints with
`COMMANDCODE_MODELS_URL` and `COMMANDCODE_API_BASE`. As in the upstream
extension, newly discovered models without a local pricing entry use zero
display cost until the pricing table is updated.

## CLI

Example commands:

```powershell
.\scripts\build.ps1
.\bin\pigo.exe list
.\bin\pigo.exe models openai-codex
.\bin\pigo.exe login openai-codex
.\bin\pigo.exe login commandcode
.\bin\pigo.exe models commandcode
.\bin\pigo.exe ask --provider kimi-coding "hello"
.\bin\pigo.exe ask --provider commandcode --model poolside/laguna-s-2.1-free "hello"
.\bin\pigo.exe ask --provider openai-codex --model gpt-5.4 "hello"
```

Build helpers:

- `.\scripts\test.ps1` runs `go test ./...`
- `.\scripts\build.ps1` runs tests and builds `bin/pigo.exe`
- `.\scripts\build.ps1 -SkipTest` builds without running tests
- `.\scripts\build.ps1 -Release` adds stripped release flags
- `.\scripts\build.ps1 -GOOS linux -GOARCH amd64` cross-builds to `bin/pigo`

## More Documentation

- [`agent/README.md`](agent/README.md): agent runtime concepts, package boundaries, and direct usage
- [`agent/docs/runtime-contracts.md`](agent/docs/runtime-contracts.md): runtime state and event contracts
- [`agent/docs/testing.md`](agent/docs/testing.md): agent validation and live-test entrypoints
- [`examples`](examples): runnable integrations and sample applications
