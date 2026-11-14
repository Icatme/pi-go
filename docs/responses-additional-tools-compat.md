# Responses gateway tool declarations

For an `openai-responses` gateway that ignores native `additional_tools` input
items, set `OpenAIResponsesCompat.SupportsAdditionalTools` to an explicit false.
Preserve the model's other compatibility fields (models returned by `GetModel`
are independent copies):

```go
model := pigo.GetModel("openai", "gpt-6-astra")
model.BaseURL = gatewayURL
compat := model.Compat.(*pigo.OpenAIResponsesCompat)
disabled := false
compat.SupportsAdditionalTools = &disabled
```

Each request then declares the complete current tool set at top level. Late
additions, removals and redeclarations use the existing tool-list resolution;
no additional_tools items are emitted. Historical tool calls/results remain
history. Mid-conversation system-message support is a separate setting and is
not disabled by this switch. Existing full-history restrictions for dynamic
tool declarations still apply; the switch does not enable previous_response_id
with a changing tool set.

Nil preserves existing behavior. True does not grant registry capabilities to
an unknown model. Endpoint URLs are not guessed or auto-classified. This switch
is not a workaround for other gateway protocol incompatibilities, and it does
not repair histories already lost by a gateway.
