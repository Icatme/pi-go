# Chat Completions URLs with query parameters

The Chat Completions URL resolver now appends its existing endpoint suffix to the URL path, not the query or fragment. This applies only when the configured base URL contains a query or fragment. URLs without either continue through the unchanged legacy resolver.

A deployment base such as `https://example.test/deploy?api-version=chosen` resolves to `https://example.test/deploy/chat/completions?api-version=chosen`. An already complete Chat endpoint is not appended again. Query bytes (including ordering, repeated keys and trailing slashes), escaped path bytes and fragments are preserved. Paths are not cleaned or resolved against another host. Malformed or opaque URLs retain the old fallback behavior; this is not new validation or support for those URL forms.

This fixes a locally reproduced URL-construction defect found while examining upstream pi issue [2528](https://github.com/earendil-works/pi/issues/2528). It does not implement an Azure provider: endpoint/API choice, deployment, API version and authentication headers remain the caller's responsibility. It does not adopt the issue's fragment workaround, translate Chat requests into Responses requests, or modify other providers' URL resolvers. No live Azure/provider request was made.
