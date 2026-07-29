# pi-go

`pi-go` is a Go reimplementation of the `pi.ai` core package surface. The repository is intentionally centered on a single reusable core library in `pkg/pigo`, with a thin CLI on top.

Current scope:

- keep provider-agnostic semantics stable first
- keep the library surface small and explicit
- focus on the most commonly used model providers, including `OpenAI OAuth` / `openai-codex`, `Kimi Coding`, `Command Code`, `Anthropic`, `DeepSeek`, `Google`, and `Mistral`
- preserve WebSocket support, observer hooks, and the single-package core-library shape

## Project Positioning

This repository is not a full agent framework. It provides the core runtime pieces needed to:

- describe models and provider capabilities
- route requests through provider and API registries
- execute `Complete`, `CompleteSimple`, `Stream`, and `StreamSimple`
- normalize message history for cross-provider replay
- expose incremental assistant events and final responses
- validate tool arguments with a small built-in schema subset

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

## Quick Start

Import path:

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

Repository layout:

- `pkg/pigo`: exported core library and tests
- `cmd/pigo`: CLI entrypoint
- `internal/cli`: auth/login and credential-store logic for the CLI only
- `docs`: repository notes and non-package documentation

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
- library runtime auth should still be passed explicitly via options or auth config

The `commandcode` provider does not run a browser login or read Command Code's
auth files. Supply `COMMANDCODE_API_KEY`, `SimpleStreamOptions.APIKey`, or an
external `AuthConfig`. Its built-in model registry is a deterministic snapshot;
constructing a provider model never performs startup network I/O.

## CLI

Example commands:

```powershell
go run .\cmd\pigo list
go run .\cmd\pigo models openai-codex
go run .\cmd\pigo login openai-codex
go run .\cmd\pigo ask --provider kimi-coding "hello"
go run .\cmd\pigo ask --provider openai-codex --model gpt-5.4 "hello"
.\scripts\build.ps1
```

Build helpers:

- `.\scripts\test.ps1` runs `go test ./...`
- `.\scripts\build.ps1` runs tests and builds `bin/pigo.exe`
- `.\scripts\build.ps1 -SkipTest` builds without running tests
- `.\scripts\build.ps1 -Release` adds stripped release flags
- `.\scripts\build.ps1 -GOOS linux -GOARCH amd64` cross-builds to `bin/pigo`
