# Managed MCP resources

`Definitions(manager, Options)` creates three virtual Agent tools without making
network requests, starting processes, or reading resources:

- `list_mcp_resources({server, cursor?})`
- `list_mcp_resource_templates({server, cursor?})`
- `read_mcp_resource({server, uri})`

The host owns the manager, identity, authorization policy, and optional artifact
sink. Hidden and disabled servers are absent from resource declarations and are
checked again when executing. Definitions freeze the manager's identity and
authorization epoch; rebuild them after a trusted scope change. Permission is
checked before setup, after setup, at physical dispatch, and before publication.
No response, reconnect, OAuth challenge, or pagination causes automatic replay.

Each list invocation returns one page and an optional `nextCursor`. Call the
same tool with that cursor for another page. The configured item/byte limits
bound each page; Agent or Codemode child-call limits bound a script's pagination.
Resource descriptions and URI templates are untrusted data, not instructions.

Reads return structured `{server, uri, contents}`. Text entries have `type:"text"`
and `text`; supported images have `type:"image"`, `data` (base64), and `mimeType`.
Image MIME, header format, dimensions, and pixel count are checked for PNG, JPEG,
GIF, and WebP. Agent results contain image parts as well as structured image
data, preserving images through the Codemode native-value projection:

```javascript
const result = await tools.read_mcp_resource({server: "files", uri: "demo://image"});
for (const content of result.contents) {
  if (content.type === "text") text(content.text);
  if (content.type === "image") image(content);
}
```

Arbitrary binary data requires an explicitly supplied `ArtifactSink`. It is
exported to the host and only a descriptor enters the model context. The
descriptor binds server, URI, MIME, byte count, SHA-256, and a digest of the
trusted identity/authorization epoch. Local paths and the raw identity remain
host-only. `ToolResult.Details` retains host artifact descriptors including paths.

`NewFileArtifactStore(existingAbsoluteRoot, maxBytes)` is an optional sink. It
uses a confined `os.Root` and generated `.blob` names. It does not derive paths
from URIs, create home/temp directories, execute HTML, or fetch linked assets.
The host controls directory permissions (including Windows ACLs), downloads,
retention, cleanup, and when to close the store. Complete exports may remain if
a later permission/scope check rejects model publication; descriptors retain
their original scope and cannot be treated as new-identity artifacts.

Resource conversion hashes its private copy of each binary artifact once for
the descriptor. Preflight reserves the fixed digest field size without reading
the payload. The built-in file store reuses that digest on this internal path;
direct `WriteArtifact` calls still check the caller's bytes against its digest.
Custom sinks receive the private copy and digest through `ArtifactSink`, and
their returned descriptor bindings are checked before publication. A digest
computed before writing does not verify disk persistence; file write, short
write, sync, and close errors are checked separately.

Limits cover items, aggregate model/structured result bytes, text bytes, decoded
blob bytes, URI/cursor length, MIME length, and image pixels. Exceeded limits
reject the result, with remote completion facts preserved. `input_required`
stays nonterminal and returns an explicit unsupported-input failure. Transport
errors and cancellation preserve uncertainty about a dispatched operation.
Resource failures use safe public messages while Go callers retain the original
error chain, including artifact-export cancellation.
