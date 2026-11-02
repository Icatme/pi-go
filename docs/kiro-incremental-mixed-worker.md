# Internal incremental text + completed tool worker

`startKiroScopedMixedStream` is a new internal path, not a registration change.
It validates limits/model/tools, snapshots the declared tool allowlist and
serializes the request before the explicit scoped credential callback. A single
injected HTTP executor receives that request; no fallback, retry or default
network exists. No tool code executes here.

The existing text-only worker and the mixed worker share one managed delivery
loop. A private terminal callback selects the existing text completion or the
new atomic tool completion mapper only after HTTP status/envelope/EOF/length and
body Close have succeeded. Leading text is actually incremental. Tool events are
not live argument fragments: they are released only from validated whole-response
evidence. Tool-only and text-only responses are also supported within the same
one-request attempt. Text after tools remains unsupported.

Preview text cannot be recalled. A late read/Close/cancellation/unsupported tool
failure has no successful final content or executable tool events. Once completion
is committed, later cancellation may discard delivery without rewriting Result;
completion evidence still does NOT authorize execution. Consumers must await a
successful final result and recheck permissions/schema/context. Agent fake tests
for late cancellation during final tool delivery remain a separate integration
step; this PR does not claim that new end-to-end coverage.

Existing cumulative byte and combined text/tool-delta budgets apply. Callers must
drain Events or Close and read Result once. Source/executor/body must cooperate
with context; arbitrary callbacks cannot be preempted. Headers contain secrets
and must not be logged. Public buffered and live-text APIs remain unchanged.

## Independently authored synthetic validation

Community baseline: pi-provider-kiro dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba,
text/tool and terminal failure scenarios. No copied source corpus or real service.

- Pipe emits text before terminal is sent, then blocks body Close: no tool event
  is observable until Close is released. Raw large integer survives result→history.
- Truncation, late Close error, cancellation and valid-framed unknown tool reject
  without tool lifecycle/Done or retry.
- Source mutation cannot replace the snapshotted tool-name allowlist.
- Tool-only and declared-tools-but-text-only terminal dispatch.
- Invalid budgets/model/schema/context reject before source; source failure never
  starts HTTP. All tokens, responses and tool results are local synthetic values.

The private completion callback must return a nonempty batch ending in Done. The
worker checks this before indexing or publishing any final events. Synthetic
malformed callbacks (empty success / ToolCallEnd-only after marking terminal)
produce an empty failure result, never a partial tool event or panic.
