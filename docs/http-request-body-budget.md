# Optional HTTP request body budget

A proxy may reject a request by byte size even when image count and token usage
are low (upstream pi issue #12105). `ApplyImageBudget` does not limit bytes.
Hosts can opt into a local HTTP preflight without trimming history:

```go
transport, err := pigo.NewHTTPRequestBodyLimitTransport(existingTransport, 10<<20)
if err != nil { return err }
client := &http.Client{Transport: transport}
// Supply client through HTTPClient in the provider options.
```

Use the product's actual limit; 10 MiB is only an example, not a provider default.
Preserve any existing client Timeout, CheckRedirect and Jar configuration. Clone
an existing client value before replacing Transport rather than mutating a shared
client in flight. Nothing enables this helper automatically.

## Contract

- Explicit positive known ContentLength is required whenever Body is non-nil and
  not http.NoBody (including an empty reader). Unknown
  and streaming lengths fail closed. Nil/NoBody is accepted only without a positive
  declared length.
- Declared excess rejects before reading. Otherwise at most limit+1 bytes are
  buffered, actual length is checked, and this wrapper closes the original body once.
  Body implementations must obey net/http's concurrent Read/Close contract.
- Successful preflight sends a cloned request with identical body bytes and a
  replayable body copy. Context and metadata are preserved; no history is modified.
- Local failures use `HTTPRequestBodyLimitError`. Reason identifies unknown length,
  excess, mismatch or body read failure. Error text excludes payload and arbitrary
  underlying text; `Unwrap` preserves body read causes for programmatic diagnosis.
  Cancellation preserves the context error. These are not upstream HTTP responses.
- This helper performs no retry. The host and next transport may have their own
  retry/redirect behavior. Existing Codex SSE regression verifies a budget rejection
  sends no upstream request. Do not infer universal provider retry semantics.

## Limits

Memory is O(limit) per concurrent request; choose a sensible cap. This counts HTTP
body bytes, not headers, decoded image data, decompressed sizes, WebSocket frames
or custom transports bypassing this client. 307/308 policy and replay eligibility
still belong to the outer client and original request. No cap is inferred from
an earlier 400/413, and no attachments are silently removed.
