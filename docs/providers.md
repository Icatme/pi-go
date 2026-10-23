# Providers and authentication

[Documentation index](README.md) · [Project overview](../README.md)

## Primary providers

- `openai-codex` via OpenAI OAuth and the Responses-style API surface
- `opencode-go` via an OpenCode Go API key and model-specific OpenAI Completions, OpenAI Responses, or Anthropic Messages routing
- `kimi-coding` via Anthropic-style Messages semantics
- `commandcode` via the Command Code Provider API, with Go-plan fallback to its generate protocol
- `anthropic` via the Anthropic Messages API
- `deepseek` via DeepSeek chat completions
- `google` via the Gemini Generative AI API
- `mistral` via the Mistral chat completions API

Supporting provider and API modules also exist for shared protocol behavior used by the core library, but the repo is intentionally optimized around these providers.

## Credentials and local files

- `.pigo/auth.json` is CLI-managed auth storage
- `.pigo/.env` is local test/helper API-key input
- `01_auth.json` is test-only and ignored by git
- library runtime auth should normally be passed explicitly via options or auth config; `commandcode` additionally supports the upstream user-home auth files listed below

## OpenAI Codex identity headers

Codex SSE and WebSocket requests merge identity headers in this order: provider
defaults, model headers, caller headers (case-insensitive). Callers can override
`originator` and `User-Agent`; authorization and account headers remain derived
from the resolved credential.
Cached WebSocket sessions use the effective originator and User-Agent identity,
and reuse requires both identity headers to match the handshake.

## Command Code

### Catalog and credentials

The `commandcode` provider uses the 2026-10-14 public catalog (89 chat models),
with capabilities and estimated prices from `pi-commandcode-provider` v0.7.7
and the latest Gemini 4 Argon/Muse Wonder metadata from `command-code` 1.82.1.
`pigo login commandcode` opens the browser-assisted login flow and
stores the returned API key in `.pigo/auth.json`. Runtime lookup also accepts
`COMMAND_CODE_API_KEY` (or `COMMANDCODE_API_KEY`), `SimpleStreamOptions.APIKey`, caller-supplied
`AuthConfig`, and the supported Command Code/pi/OMP auth-file shapes under the
user home directory: `.commandcode/auth.json`, `.omp/agent/auth.json`, and
`.pi/agent/auth.json`.

### Protocol routing and replay

Discovery selects the wire protocol from `supported_endpoints`: Messages,
then Responses, then Chat Completions. Models advertising only non-chat
endpoints (such as `/systemone`) are excluded. The selected protocol is stored
in `Model.Compat` as `CommandCodeCompat` and retained in the catalog cache.
Catalogs or manually constructed models without endpoint metadata keep the
Claude Messages / other-model Chat Completions selection.
Only an HTTP 403 with
`upgrade_required` selects `/alpha/generate`; authentication errors, network
failures, rate limits, and other provider errors do not trigger that fallback.
The selection is isolated by endpoint and credential.
Models keep `API=commandcode-custom` for routing; returned assistant messages retain
their actual wire API so reasoning signatures replay only across matching protocols.
The Responses adapter preserves Command Code's incremental reasoning events,
uses system-role prompts and omits optional reasoning controls for `off`.
Requested reasoning levels are clamped to the model's advertised efforts on
both native and generate routes (for example, Qwen 3.8 `high` selects `xhigh`).
Claude signed thinking blocks are preserved for native replay even when their
visible thinking text is empty.

### Image preparation

For Command Code `gpt-6-luna`, PNG inputs are converted to JPEG before sending
to avoid an upstream PNG recognition defect observed on 2026-10-14. This applies
to user and tool-result images on all routes: dimensions are preserved, JPEG
quality is 95, and transparency is composited onto white. Conversion is lossy;
the caller's original image data remains unchanged. Invalid PNGs fail locally.
Local conversion limits are 32 MiB of PNG data, 64 Mi pixels, and 65535 pixels per
side. Other models and image formats are sent unchanged.

### Usage, errors and retention

Unknown dynamic model capabilities remain unknown; their zero price estimate
does not mean free usage. Prices are display snapshots, including context-based
tiers; DeepSeek estimates use off-peak rates rather than live billing rates.
Generated responses that
report an upstream/network failure in their finish metadata are errors.
Completed reasoning is not replayed through the generate protocol, and missing
tool results are represented explicitly. `CMD_ZDR=1` (or `COMMANDCODE_ZDR=1`) requests strict zero data
retention through the provider's `x-cmd-zdr` header.

### Discovery and caching

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

Account balances and usage windows are covered in [Account quota queries](account-quotas.md).

## OpenCode Go

The `opencode-go` provider follows Pi's built-in OpenCode Go routing and reads
`OPENCODE_API_KEY`. Its deterministic built-in catalog is the 2026-08-20
snapshot of active, non-deprecated, tool-capable models from
[`models.dev`](https://models.dev), which is also Pi's catalog source. OpenCode
Go keys are managed at [`opencode.ai/auth`](https://opencode.ai/auth).

OpenCode Go sends `x-opencode-session` when `SessionID` is provided, across all
three wire protocols; an explicit caller header takes precedence. For native
OpenAI GPT-5.6 models and GPT-6 Astra, long prompt caching uses
`prompt_cache_options.ttl: "30m"`; disabling caching uses explicit mode without
an automatic cache key. Earlier models retain their supported cache format.
Custom Responses models opt in through `SupportsExplicitPromptCacheMode` and
can disable `max_output_tokens` with `SupportsMaxOutputTokens`.

## Checked-in catalog snapshots

The September 2026 catalog adds GPT-6 Astra and uses `deepseek-flash` for native
DeepSeek V4.1 Flash, including image input. Retired `openai-codex` GPT-5.4 and
GPT-5.4 mini entries are removed; the CLI and examples now default to GPT-5.5.
The OpenAI API entries and other providers' DeepSeek model identifiers are
independent and remain available. Astra supports `low` through `max`; Off is
clamped to a supported level by the built-in agent backend.
