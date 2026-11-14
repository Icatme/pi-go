# Google function parameter JSON Schema opt-in

`GoogleGenerativeAICompat.SupportsParametersJSONSchema` selects the function
parameter wire format for `google-generative-ai`:

- `nil` or `false`: preserve the legacy `parameters` field.
- `true`: send only `parametersJsonSchema`, never both fields.

This option is independent of `SupportsMultimodalFunctionResponses`. It does not
activate features by provider name, model name, hostname, or catalog entry.
Hosts must confirm their endpoint and model support the new field before opting
in. Existing custom gateways and the default path remain unchanged. The legacy
path is not a guarantee of full JSON Schema support.

The original `Tool.Parameters` is forwarded without schema rewriting or local
expansion. A nil schema retains the existing empty-object default in the selected
field. This does not guarantee the server accepts every JSON Schema keyword,
resolve references remotely, or add output-schema validation. No Vertex or Cloud
Code Assist implementation is introduced.

Official FunctionDeclaration reference (checked 2026-11-14):
https://ai.google.dev/api/caching#FunctionDeclaration
It documents `parametersJsonSchema` and `parameters` as mutually exclusive.
Upstream issue: https://github.com/earendil-works/pi/issues/1398

Offline tests inspect actual HTTP request JSON only. No live Google calls or
server-rejection reproduction were performed. This change is stacked on PR236,
which introduced `GoogleGenerativeAICompat`; merge that base first.
