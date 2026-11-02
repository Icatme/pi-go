# Completed tool response host queue (draft)

Depends on #90. Internal synchronous buffered bridge only: no provider registration,
HTTP, credential access, tool execution or agent runtime wiring.

## What is guaranteed

Before creating a stream, the complete response passes #89's TOOL_USE evidence,
tool-name, argument, history and usage checks, then #90's lifecycle projection.
Invalid/cancelled input, more than 682 parts or excessive delta count is rejected.
Every detached event is re-accounted against caller bytes with an additional
reserved aborted terminal. No partial batch reaches a queue on preflight failure.

The completed events are synchronously enqueued into the existing managed host
stream. Per-event context checks stop publishing on construction-time cancellation
and finish with Aborted. A result can be consumed before Events without dropping
events because pending count, delta count and bytes were all preflighted. This is
**buffered completion replay**, not realtime tool argument streaming.

Result, events and evidence have independent maps. Consume Events or call Close,
and read Result only once, matching the existing host contract.

## Execution and cancellation boundary

A ToolCall / toolcall_end is data, NOT approval to execute. An agent must check
current context, tool schema and permissions immediately before executing. No
execution callback is accepted by this bridge.

Cancellation after synchronous completion clears queued delivery and reports an
aborted event, but cannot revoke a previously completed Result or already delivered
tool. Close similarly stops delivery without rewriting completed evidence. This
is the existing host contract, explicitly tested rather than hidden. It must not
be advertised as automatic cancellation of tools. This adapter is not yet connected
to an agent or real Kiro request, so no tool side effect can occur here.

Input ownership is non-concurrent; the response must originate from a collector
after HTTP length/Close success. Evidence struct provenance is a caller contract,
not cryptographic proof. A future HTTP tool provider must enforce that sequencing.

## Validation scenarios

Five independent tests cover:
- exact event lifecycle + detached result, with Result read before Events;
- cancelled/nil context, failed terminal rejection and exact abort-byte reserve;
- all 128 allowed calls with 386 delivered events and zero drops; 683-part reject;
- completion followed by parent cancellation: aborted delivery, retained evidence;
- concurrent idempotent Close with no remaining Events.

Existing collector/projection tests cover synthetic EOF/tool terminal failures.
The mid-enqueue cancellation branch is statically reviewed; deterministic tests
here cover preflight and after-completion cancellation, not that exact timing.
No real Kiro capture, account, network or tool is used.
