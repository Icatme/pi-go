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

## Agent history

The Agent retains the current tool argument object across turns and session
restore. Multi-turn Agent use requires explicit registration for caller-authored
history. Direct pigo callers can select strict original-wire replay when they
retain the provider's raw arguments. The [migration guide](kiro-agent-authored-compatibility.md)
lists both registration entry points and the persistence guarantees.
