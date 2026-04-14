# pi-go

`pi-go` is a Go port of `V:\gitdownload\pi-mono\packages\ai`.

Repository layout:

- `cmd/pigo`: CLI entrypoint
- `internal/cli`: CLI-only login and credential-store logic
- `pkg/pigo`: library code and tests
- `docs`: project notes and repo-level documentation
- repo root: module files and top-level docs only

Current approach:

- migrate provider-agnostic tests first
- keep the first implementation surface small
- add provider integrations after the core semantics are locked down
- for now, only implement `OpenAI OAuth` and `Kimi Coding`

Initial migrated areas:

- model metadata and `SupportsXHigh`
- provider/model registry helpers and target-provider model metadata
- lazy provider-module registry for plugin-style provider wiring
- target-provider auth resolution primitives for `openai-codex` OAuth and `kimi-coding` API keys
- minimal `Complete` / `CompleteSimple` path for `kimi-coding` via Anthropic-style Messages API
- minimal `Complete` / `CompleteSimple` path for `openai-codex` via Responses API
- provider-native `Stream` / `StreamSimple` event surface for the supported providers
- provider `responseId` propagation and aborted-request normalization
- overflow detection
- message transformation for cross-provider replay
- minimal tool argument validation fallback

Package import path:

```go
import "github.com/Icatme/pi-go/pkg/pigo"

Context serialization:

- `Context` now supports standard JSON round-tripping via `json.Marshal` / `json.Unmarshal`
- `SerializeContext` and `DeserializeContext` provide explicit helpers for persistence boundaries
- serialized tools preserve name/description/parameters; runtime validators are intentionally omitted
- messages and content blocks are encoded with explicit role/type tags so conversations can be restored safely
```

Provider module registration:

- built-in providers are registered through a lazy provider registry
- external providers can register with `RegisterProviderModule` or `RegisterLazyProviderModule`
- `Stream` / `Complete` resolve provider execution through that registry instead of a hard-coded switch
- provider auth metadata and provider capability metadata are also attached to registry modules
- runtime auth resolution now reads provider auth behavior from the registered module

API registration:

- runtime stream dispatch is registered by `model.API`, not directly by provider
- built-in APIs are registered lazily through an API registry
- `Stream` / `Complete` are the provider-option path
- `StreamSimple` / `CompleteSimple` are the generic simple-option path
- simple options are normalized into provider options through a dedicated simple-options layer

Support-file boundaries:

- `.pigo/auth.json` is ignored by git and written by `pigo login`
- `.pigo/.env` is ignored by git and used for local API-key lookup in tests/helpers
- `01_auth.json` is ignored by git and used only by live tests
- runtime auth must be passed explicitly and does not read those files

CLI usage:

```powershell
go run .\cmd\pigo list
go run .\cmd\pigo models openai-codex
go run .\cmd\pigo login openai-codex
go run .\cmd\pigo ask --provider kimi-coding "hello"
go run .\cmd\pigo ask --provider openai-codex --model gpt-5.4 "hello"
.\scripts\build.ps1
```

CLI behavior:

- `list` shows OAuth-login providers only
- `models` shows available models for a provider
- `login` stores OAuth credentials in `.pigo/auth.json`
- `ask` loads OAuth credentials from `.pigo/auth.json` and API keys from `.pigo/.env`

Build scripts:

- `.\scripts\test.ps1` runs `go test ./...`
- `.\scripts\build.ps1` runs tests, then builds `bin/pigo.exe`
- `.\scripts\build.ps1 -SkipTest` builds without re-running tests
- `.\scripts\build.ps1 -Release` adds stripped release flags
- `.\scripts\build.ps1 -GOOS linux -GOARCH amd64` cross-builds to `bin/pigo`
