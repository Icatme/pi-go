# Completed-text host stream bridge

`newKiroCompletedTextStream` turns a validated, completed text response into the
existing `AssistantMessageEventStream` delivery interface. It is unregistered and
never opens a network connection or discovers credentials. This is **buffered
replay after completion**, not incremental token streaming or a usable API factory.

Projection completes synchronously before a stream is created. Unsupported input,
nil/already-cancelled context, partial usage, omitted terminal proof and budget
failure return a nil stream. It reserves all event counts and conservative bytes
before enqueueing (including an extra byte allowance for an abort terminal), so callers need not drain Events concurrently with construction.
Three events per part plus start/done must fit the host 2048 pending-event limit;
at most 682 deltas is below the separate 1024 delta limit. The independent 32 MiB
snapshot budget can reject **fewer** than 682 parts, so 682 is not a guaranteed
capacity. No limit is raised and no event is intentionally dropped. The existing
host dispatcher is used; no extra producer goroutine or callback is introduced.

After construction, callers may read Result before Events. Result is single-use,
not an idempotent getter. Consumers must drain Events or Close the stream. Close is
concurrent/idempotent and releases queued delivery; it preserves completed result
evidence, matching existing host behavior. Cancellation during synchronous enqueue
produces an aborted result; cancellation after completion stops delivery but does
not rewrite the finished Result. Consequently **Events may end aborted while
Result still contains the successful completed message**. No atomic completion vs
cancellation guarantee is claimed. Tests explicitly exercise this distinction.

Inputs must not be mutated concurrently during construction; output snapshots are
owned/detached. No observer callbacks are installed, and no request payload/token
is published to observers. Model identity/time are explicit caller metadata, not
validated account/model availability.

## Tests and remaining gates

The earlier pinned upstream text lifecycle scenario from
[stream.test.ts L954–988](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/test/stream.test.ts#L954-L988)
continues through the projector. New tests verify delivery matches that exact
projected event sequence, final result ownership, nil/cancelled preflight, unsupported
context usage, event-count/buffer budget rejection, many pending events without
drops, concurrent Close, and cancellation after Result but before Events.
The enqueue-loop cancellation branch has been statically reviewed but is not
deterministically exercised by these tests; race-detector success is not branch
coverage. Abort-terminal byte reservation has a separate tight-budget regression.
These Go lifecycle tests are independently authored host integration tests, not
claims of migrating upstream's real-time latency/cancellation implementation.

Real provider registration still requires explicit configuration and HTTP injection,
no accidental default transport, contextUsage/unknown-usage representation, tool
history evidence, reasoning signature replay and live service contract validation.
This bridge does not weaken any of those gates or register a default provider.
