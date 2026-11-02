# Explicit live-text registration

`RegisterKiroLiveTextModule(sourceID, KiroLiveTextConfig{...})` installs only an API
module. This is a distinct public entry point, not a new behavior hidden in a
buffered option. Existing buffered config/option layouts and behavior are unchanged.

Supply API/provider identity, region/profile, origin/user agent, per-attempt IDs,
scoped credentials and an injected HTTP executor explicitly. Registration validates
without invoking callbacks. There is no default network, discovery/login, refresh,
retry/fallback, model catalog or service permission grant. RequestIDs can run before
input preparation; it is trusted and may have side effects even when later input
is rejected. Credential callback runs only after validation and serialization.

Use the ordinary `Stream` or `StreamSimple` dispatcher with active RequestContext
and SSE (or the dispatcher's accepted empty selection). Auto, WebSocket, SessionID
and unsupported host options reject. Raw `GetAPIModule().Stream` calls bypass the
host's option-validation layer and are not the supported entry point.

This first live registration is deliberately text-only: it advertises custom tools
as unsupported, has no schema catalog and rejects declared tools before source or
HTTP. Tool history, images, reasoning/signatures and unsupported protocol semantics
remain rejected by the strict projection/collector. It never switches to buffered
mode on an unsolicited tool response or retries a response after previews.

TextDelta events represent actual incremental delivery and may precede terminal
metadata. They are provisional, not successful completion. A late read, terminal,
length, Close or cancellation failure produces failure rather than successful
final content. Trusted callbacks/body operations must cooperate with context;
blocked arbitrary code cannot be preempted. Drain Events or Close; read Result once.

## Independent tests

Public full/simple dispatch each uses a pipe withholding terminal bytes until a
TextDelta is received. Tests also check no registration callbacks, unsupported tool
capability, guarded inputs before credentials and one-attempt late Close failure.
These extend independently authored upstream incremental/error scenarios from
pi-provider-kiro dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba, not copied tests.
All wire data and credentials are synthetic. Real endpoint acceptance, client
identity, account scope and service billing have not been validated.

Already delivered preview text cannot be recalled from the consumer/UI. Consumers
must visibly retain its provisional status until completion. This API is not yet
an out-of-the-box Agent registration: the Agent normally supplies SessionID and
Auto, both rejected here. The existing buffered session/Auto registration remains
the tested Agent route; adding equivalent explicit live options is separate work.
