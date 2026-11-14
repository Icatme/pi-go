# Dedicated DeepSeek endpoint query preservation

The dedicated DeepSeek Chat resolver now appends the endpoint to the parsed path when the configured base URL includes a query or fragment. Root URLs retain DeepSeek's existing /chat/completions default, not OpenAI's /v1 default. Complete endpoints are not appended again. Raw query ordering, duplicate keys, trailing query slashes, escaped path bytes and fragments are preserved without path cleaning.

URLs without query/fragment, and malformed/opaque inputs, retain the old resolver behavior. This does not add URL validation, infer authentication headers or parameters, change provider identity, or rewrite other providers. Query values in tests are synthetic localhost fixtures, not actual DeepSeek-required API parameters.

This independently reproduces the same URL-construction bug class found while examining pi issue2528 for Chat Completions. The issue concerned Azure, not a report of a DeepSeek service failure. No real provider call was performed. Earlier DeepSeek replay/EOF/usage fixes are separate changes; this branch does not replace or subsume them.
