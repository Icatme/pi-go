# Google streaming base URL query handling

The Google Generative AI transport sends `streamGenerateContent` requests using SSE. Google's official REST streaming cookbook specifies `alt=sse`:
https://github.com/google-gemini/gemini-api-cookbook/blob/main/quickstarts/rest/Streaming_REST.ipynb
Endpoint reference: https://ai.google.dev/api/generate-content#method-models.streamgeneratecontent

For an absolute HTTP(S) base URL containing a query or fragment, the endpoint suffix is appended to the escaped path, not to the query or fragment. Existing percent-encoded path bytes and query ordering/duplicates remain unchanged. A fragment remains a URL fragment and is not transmitted by HTTP.

When no decoded `alt` key is present, the transport appends `alt=sse`. If `alt` already occurs, every decoded value must be exactly `sse`; all original query bytes are retained. Conflicting, empty or malformed query parameters fail locally with provider failure reason `invalid_stream_query`, without issuing HTTP or including the query in the diagnostic. This does not silently override a caller's explicit non-SSE request.

Base URLs without `?` or `#` keep their prior construction behavior. Invalid, opaque, relative or non-HTTP(S) base URLs remain on the legacy path; this is not a new general URL validator. The base URL remains a prefix, not a complete `streamGenerateContent` endpoint. Model ID handling, default base URL, authentication headers, model discovery and retries are unchanged. This does not introduce Vertex routing or arbitrary gateway compatibility.

Validation uses synthetic keys and localhost HTTP only, including exact path/query bytes, encoded slash and alt keys, repeated parameters, authentication preservation and one-request success / zero-request configuration failure. No real Google request or billing was used.
