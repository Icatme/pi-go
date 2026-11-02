# Incremental validated text observation

`collectKiroResponseWithTextObserver` adds an optional synchronous text callback
to the existing stream collector. It reads caller-provided `io.Reader` bytes and
has no HTTP, credentials, registry, tool executor or background goroutine. The
existing `collectKiroOfflineResponse` wrapper supplies nil and retains its fully
buffered contract, including zero result on any failure.

This callback is genuinely **incremental at the validated wire-frame boundary**:
CRC, framing, envelope, strict JSON, allowed ordering and text/payload/event budgets
must pass before a text frame is observed, and the callback finishes before the
next wire frame is read. A gated-reader test makes the next segment unavailable
until the first callback occurs; replaying callbacks after EOF cannot pass it.
This is not a claim that the host registered provider or HTTP adapter now delivers
real-time tokens. Existing HTTP attempt and host buffered bridge remain unchanged.

Only assistant text fragments are observed, in original order, including repeated
or empty fragments. Reasoning, signatures, tools, context percentages, usage and
terminal success are not callback payloads. Tool evidence remains staged until
explicit completion + whole-reader validation; no tool is executed. Final output
matches the nil-observer path when successful.

**Text callbacks are provisional.** Later checksum errors, truncation, service
exceptions, terminal conflicts or missing stop evidence still return a zero final
response and an error, but already observed text cannot be retracted. Consumers
must not interpret previews as successful completion or executable tool intent;
they must surface the final outcome separately. No success callback is synthesized.

Callback errors stop reading immediately and return a fixed observer error without
embedding caller error text. Cancellation is checked before and after each callback
and retains its fixed classification. The observer is trusted to honor cancellation
and return; a blocked callback or reader cannot be preempted by this synchronous
helper. Panics are not recovered. Reader ownership stays with the caller, including
Close. Inputs/callback-owned state must not be concurrently mutated unsafely.
The existing collector still retains bounded response evidence in addition to any
consumer-owned preview storage; this is not a new streaming-memory/RSS guarantee.

## Independently authored scenarios

Pinned community [stream.test.ts L954–988](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/test/stream.test.ts#L954-L988)
checks text delta ordering/accumulation. Our synthetic repeated Unicode frames
carry this observable invariant through incremental delivery and compare the final
result with the existing buffered collector. Unlike upstream's percentage-based
termination fixture, explicit metadata stop is required here. The gated reader is
an original Go causality test, not copied upstream code or a live latency measure.

Additional tests exercise preview followed by CRC/truncation/exception/conflicting
stop failures; reject invalid CRC/duplicate JSON/surrogate/order/budget before any
callback; verify callback failure/cancellation does not read the next segment;
exclude reasoning/tools from observations and preserve caller reader ownership.
No fake test is represented as a real authenticated service call.
