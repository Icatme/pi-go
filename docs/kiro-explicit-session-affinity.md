# Explicit session-to-conversation affinity

Depends on #101. The original KiroBufferedConfig field layout and default
RegisterKiroBufferedModule behavior remain unchanged (nonempty SessionID rejects).
The separate RegisterKiroSessionBufferedModule(source, config, affinity) opts into
session mapping. It still uses completed buffered delivery, not realtime output.

The caller supplies KiroSessionAffinity{Tenant, Key}. Tenant is a required bounded
namespace; Key is 32..64 bytes of caller-managed secret entropy. Length is checked,
entropy cannot be verified. No key generation/discovery/persistence is performed.
Registration copies key bytes. JSON omits Key; fmt String/GoString redact the
whole affinity object. The callback/registry closure retains the key in memory;
there is no memory zeroization guarantee. Do not log individual Key bytes.

Conversation ID = "pi-" + hex(HMAC-SHA256(key, length-prefixed fields)) over:
- version/domain "pi-go/kiro-session/v1";
- tenant, API route, provider route, region, profile ARN, host SessionID.

Every field has an 8-byte big-endian UTF-8 byte-length prefix. Fixed framing avoids
concatenation ambiguity. Same material yields stable IDs without a mutable mapping
or implicit random reset; changing tenant/key/routing/region/profile/session creates
a different derived ID. Scope/tenant labels are caller assertions, not verified
IAM or authorization. Reusing the same tenant/key/scope/session intentionally
reuses a conversation, so callers must assign distinct tenant namespaces.
Key rotation or configuration changes break affinity; migration is caller-owned.
The resulting opaque ID is still sensitive correlatable metadata, not anonymous.
The derived pi-hex shape has only offline validation; service acceptance of that
shape has not been verified. Do not infer live compatibility from these tests.

This mode requires a nonempty valid UTF-8 SessionID <=256 bytes, no controls or
surrounding whitespace. It is never trimmed, truncated, logged or put on the wire
as a separate session field. The normal option hook checks it before identity/
credential/executor callbacks. All other previously unsupported options still
reject. RequestIDs must supply invocation identity; ConversationID may be empty
or exactly equal to the derived ID. A conflicting explicit ID fails before
credentials/HTTP instead of silently replacing a requested conversation.
Invocation uniqueness is still caller-owned; no automatic UUID is generated.

Full Stream and StreamSimple use the same immutable registered affinity copy.
Existing raw GetAPIModule callback bypass caveat remains: supported dispatch is
through host Stream/StreamSimple. All callback state must be concurrency-safe.
Registration/unregistration does not cancel captured in-flight calls.

Four original fake tests cover an independently Python-computed HMAC vector,
cross-scope/tenant/key/session isolation, length-framing ambiguity, secret
formatting, eight concurrent invocations with two stable conversations, key-copy
isolation, conflicting IDs and invalid sessions, unchanged default mode, and
required explicit key/tenant material. No real credentials/service/tool execution.

This closes host SessionID mapping only. Arbitrary agent options, dynamic system
changes, persisted raw history, live account validation and realtime parity remain
separate limitations. This feature is not permission to use a private endpoint.
