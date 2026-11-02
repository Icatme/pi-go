# Explicit system prompt lowering policy

Depends on #96. Default behavior still rejects nonempty Context.SystemPrompt.
A caller must explicitly set kiroHTTPRequestOptions.SystemPromptPolicy to
prepend_first_user to enable this transformation. The reject policy is equivalent
to the default; unknown policies fail even when no system prompt is present.

## Semantics and evidence

Community upstream pinned at dd93f2e prepends system text to the first user content:
[transform.ts L204–232](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/transform.ts#L204-L232),
with a current-user fallback in
[stream.ts L865–880](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/stream.ts#L865-L880).

This is a semantic downgrade to ordinary user text, NOT native system priority,
instruction isolation or a claim the model must honor it. No XML wrapper,
special role, unofficial priority guarantee or developer-role conversion is added.
Applications requiring native system/developer semantics must keep the default
reject policy. Opt-in is per request and never selected by registration defaults.

The helper reports a boolean when lowering occurs. The HTTP builder consumes that
explicit choice to construct the body; no diagnostic header is sent and no new
response diagnostic is invented. A future public provider must expose the chosen
policy/lowering diagnostic appropriately instead of silently selecting it.

## Strict transformation

Only Context.SystemPrompt is lowered. The original first message must be a user
value or nonnil pointer with nonempty pure text; permitted text blocks concatenate
without extra separators. Images, signatures, thinking and unsupported content
are rejected. Original user text must be nonblank before adding instructions.

Result = original system text + two newlines + original first-user text.
The copied Context clears SystemPrompt and replaces only its first user message.
The original message slice, first pointer and other history are not modified;
remaining messages are read-only shared inputs until immediate serialization.
Calling again with the original Context does not accumulate prefixes.

System+separator+first-user bytes are bounded before allocation; final strict
request projection still budgets all text/history/schemas, and serialized output
retains its separate wire-byte limit. At most 4096 messages and 4096 first-user
text blocks are processed by the helper; those blocks collapse to one string.
Existing later projection has its own block budget. UTF-8 is validated, never
repaired by replacement.

With tool history, the prefix stays on the first historical user, not the current
empty tool-result carrier. Later/embedded SystemMessage objects, dynamic tool/system
changes and developer-role messages are not silently normalized and remain rejected
by the existing strict request projection. This is not general transcript support.

## Independent tests and registration status

Four tests cover value/pointer/text-block handling, exact budget, immutable/repeated
inputs, explicit/default/unknown policy, signature/image/blank/UTF8 rejection,
text and tool wire shape, tool-result carrier preservation and credential-source
not invoked for rejected lowering.

No provider is registered or enabled. Production module configuration still needs
an explicit host-option matrix (HTTPClient, generic auth, hooks, sampling, retries,
timeouts, sessions and response formats), identity/transport ownership and login/
account verification. This change adds no login, credential lookup, refresh, network,
tool execution, CI or deployment.
