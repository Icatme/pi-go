# Anthropic-compatible endpoint query preservation

Anthropic Messages and the Kimi hosted request path now append their existing
`/v1/messages` suffix to the URL path when BaseURL is an absolute HTTP(S) URL
containing a query or fragment. Raw query bytes (including repeated keys),
encoded path separators and fragments are preserved in URL construction;
fragments are not sent in HTTP request targets.

This does not infer API routing, normalize away an existing `/v1`, accept full
endpoint overrides, or change authentication, request bodies, custom clients,
retries or usage. URLs without query/fragment and malformed, opaque or relative
inputs retain the old construction behavior. This is not a new URL validator.

Upstream #10992 concerned remote catalog OpenAI metadata mixed with Anthropic
routing; its maintainer reported that catalog corrected. Go built-in Kimi
already has `/coding` and Anthropic Messages. This separate query/fragment
defect was reproduced with synthetic localhost requests only. No vendor
endpoint, credentials or paid provider was used.
