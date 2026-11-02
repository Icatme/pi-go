# Completed text/tool response dispatch (draft)

Depends on #92; adds an internal synchronous buffered entry point, not provider
registration and not a merger with the realtime text observer. Existing tool-only
entry point remains strict and still rejects normal text completion.

A request may declare tools and receive plain text. After the complete shared HTTP
validation (including Close), explicit Stop/Length routes to the completed-text
queue, and ToolUse routes to the completed-tool queue with the snapshotted request
allowlist. No declared tools means an unsolicited tool response fails. Unsupported
terminal reasons never select a success path. No stop reason is inferred from EOF.

The collector's MAX_TOKENS policy removes staged tool evidence. Dispatch preserves
that behavior: only validated text reaches a Length message/queue; no tool lifecycle
event survives. An END_TURN with staged tools is contradictory and fails, as do
content filtering and truncated TOOL_USE frames. Usage, thinking, context and queue
limits retain the underlying strict subsets. No schema or permission check is
silently bypassed by selecting a route.

Four independently authored synthetic test functions cover text END_TURN and
MAX_TOKENS with/without declarations; explicit/unsolicited tools; staged tools
followed by truncation/filtering/conflicting stop; and old tool-only compatibility.
No live service, credentials or actual tool execution occurs.

## Agent integration gate found by inspection

Current agent/pigo_stream_model.go is not yet an evidence-preserving Kiro bridge:
- convertPigoToolCall marshals call.Arguments, not original RawArguments;
- convertAssistantMessageToPigo does not populate pigo.ToolCall.RawArguments.

Therefore the package-level exact history roundtrip must NOT be advertised as a
working agent multi-turn replay. Existing agent cancellation/schema/permission
checks do not fix this evidence loss. Re-serializing a parsed map as purported
original wire evidence would be incorrect.

A separate change needs an explicit raw-provider-evidence channel, clear handling
of original versus edited arguments, detached ownership and persistence rules.
Generic agent Arguments is currently also canonical execution data, so repurposing
it silently would blur these identities. Before any provider registration, test
provider → agent → history with large numeric literals, edits, permission rejection,
cancellation, persistence/restore and missing evidence. No broad agent/provider
semantics are changed in this PR.
