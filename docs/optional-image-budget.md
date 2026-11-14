# Optional host-selected image budget

`pigo.ApplyImageBudget` is an opt-in preparation helper, not an automatic provider
policy. It has no gateway-specific limit. The default `ImageBudget{}` disables it.
The motivating upstream report is [Pi #10162](https://github.com/earendil-works/pi/issues/10162):
a long screenshot task exceeded one gateway's image-count limit. That report does
not establish a universal model limit.

After converting host messages to canonical `pigo.Message` values, the host supplies
`MaxImages` from its own product/provider configuration and `PruneBefore` as an
exclusive message index. Only older tool-result images before that boundary may be
replaced. All user and assistant images, and every image at/after the boundary,
are protected. The host must include every current-input batch and needed current
tool observation in that protected suffix. Do not guess this boundary from the
last user message when a request may contain multiple queued inputs.

```go
prepared, err := pigo.ApplyImageBudget(request.Messages, pigo.ImageBudget{
    MaxImages: configuredImageLimit,
    PruneBefore: firstProtectedMessageIndex,
})
if err != nil {
    return err // do not send or retry an over-budget request
}
request.Messages = prepared
```

Oldest eligible images are replaced first with an explicit text marker. Messages,
call/result associations, other content and metadata remain intact. If protected
images alone exceed the limit, `*pigo.ImageBudgetExceededError` reports the limit
and protected count; no partial output is returned. Negative limits and invalid
boundaries fail. Enabled policies reject pointer/nil messages, pointer/nil blocks,
and noncanonical user content instead of silently undercounting images.

The helper uses copy-on-write; unchanged nested content/metadata remain shared and
must be treated as immutable. It does not modify persisted history. It is not a
byte/token budget, image decoder, compaction engine, or automatic retry mechanism.
Reapply with the host's chosen configuration on model changes. A later hook that
adds images can invalidate the budget, so apply it after content-producing hooks.

`agent.TransformContext` uses a different message representation and does not
expose a reliable current-input batch boundary or final resolved model. This helper
is therefore deliberately not installed there automatically. Host adapters should
call it at their final pigo request boundary; SuperAgent admission/recovery defaults
remain unchanged. No claim of live provider acceptance is made.
