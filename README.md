# pi-go

`pi-go` is a Go port of `V:\gitdownload\pi-mono\packages\ai`.

Current approach:

- migrate provider-agnostic tests first
- keep the first implementation surface small
- add provider integrations after the core semantics are locked down
- for now, only implement `OpenAI OAuth` and `Kimi Coding`

Initial migrated areas:

- model metadata and `SupportsXHigh`
- provider/model registry helpers and target-provider model metadata
- target-provider auth resolution primitives for `openai-codex` OAuth and `kimi-coding` API keys
- minimal `Complete` / `CompleteSimple` path for `kimi-coding` via Anthropic-style Messages API
- minimal `Complete` / `CompleteSimple` path for `openai-codex` via Responses API
- provider-native `Stream` / `StreamSimple` event surface for the supported providers
- provider `responseId` propagation and aborted-request normalization
- overflow detection
- message transformation for cross-provider replay
- minimal tool argument validation fallback
