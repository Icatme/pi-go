# Opt-in Google function-response images

Google's GenerateContent protocol has `FunctionResponse.parts` for media that
belongs to a particular function result. The official Gemini guidance describes
multimodal function responses for Gemini 3 and later; it does not establish this
support for old Gemini models or arbitrary Google-compatible gateways.

Enable the encoding only after verifying the selected model and endpoint:

```go
enabled := true
model.Compat = &pigo.GoogleGenerativeAICompat{
    SupportsMultimodalFunctionResponses: &enabled,
}
```

With that explicit opt-in, each tool result's images are placed inside its own
`functionResponse.parts`, alongside the same ID, function name and structured
output/error. Parallel results remain in their original order, with no shared
sibling images or duplicate copies. Multiple images retain their order within
the result. The existing image-input support check still applies.

Nil/false deliberately preserves the legacy sibling-image wire representation
for compatibility. This change **does not fix or promise structural image
ownership on that default path**. It does not automatically modify any model
catalog, prove gateway support, add image fetching, or enable tool images in
AI-relay. Hosts must choose the supported path explicitly.

The Google guide lists PNG, JPEG and WebP images for this feature; callers remain
responsible for valid image bytes/MIME types and model availability. This patch
does not add general PDF/audio/video support. The protocol requires unique
media display names only when structured response values use references to the
media; this adapter does not emit such references. No image data is added to
logs or diagnostics.

Sources checked 2026-11-14 (a separate publication date was not exposed):
- GenerateContent API reference, FunctionResponse and FunctionResponsePart:
  https://aistudio.google.com/api-reference/generate-content
- Google Cloud function calling, multimodal responses (GenerateContent, not
  Interactions `function_result`):
  https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/tools/function-calling

Validation is offline JSON/request construction, not real model acceptance.
