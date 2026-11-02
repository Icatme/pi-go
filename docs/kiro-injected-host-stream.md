# Explicit injected incremental host stream

`startKiroInjectedTextStream` now joins the request builder, single-attempt HTTP
boundary, incremental text mapper and actual `AssistantMessageEventStream` queue.
It is unregistered/internal and **never chooses credentials or a network executor**.
Tests use fake executors and local in-memory pipes only. This is working incremental
host delivery with injected responses, not proof of real service compatibility or
permission to call the community private runtime.

## Construction and execution boundary

Construction validates explicit context/executor/limits/model identity, requires
host model ID to match the exact wire model ID, rejects tool catalogs, and serializes
text-only conversation into an owned request before starting a worker. Unsupported
history/options fail before execution. Model fields supply identity only, not inferred
capabilities/prices/headers. Caller inputs must not be mutated during construction;
after it returns, changing the original conversation cannot change the worker's body.
Tokens remain explicit transient request headers, not configuration/env/cache lookup;
no request, headers or raw error is logged. Go memory is not guaranteed zeroized.

The HTTP core is extracted into a private prepared-request executor used only by
validated builders, not offered as an arbitrary-URL public API. It preserves the
existing limits, fixed runtime URL construction, one attempt, no redirects/retries,
preview wire-probe guard and response/request cleanup. The worker observes validated
text into a bounded mapper and pushes real host lifecycle events immediately. Only
after full HTTP validation **and Close** succeed can it complete text_end/done.
Unexpected tool/reasoning/context usage/partial usage responses remain unsupported:
previous text may be visible, but final output is Error and no tools are released.

## Delivery, cancellation and bounds

One producer worker plus the existing host dispatcher is used. The mapper bounds
cumulative event bytes (including a reserved error), text and fragment count; the
current queue's 1024 delta / 2048 pending-event limits accommodate at most 1024 deltas
plus start/text_start/text_end/done. The 1025th fragment is rejected with Error
and no Done, also covered through the actual queue. This uses limits, not unbounded buffering or
intentional delta dropping. Separate byte caps may reject earlier. Slow-consumer
regressions read Result before Events at 128 and 1024 deltas and require zero drops.
These are payload bounds, not RSS or wall-clock guarantees.

Consume Events or Close, and read Result once. Parent cancellation/Close cancels
the worker context; the executor, Read and Close must cooperate. Blocking trusted
code cannot be preempted, and panics are not recovered. Close stops delivery and does
not wait for an uncooperative producer; Result can still wait for it. A pipe-backed
regression waits until Read is actually blocked and verifies cooperative cancellation
and explicit Close release it and finish Aborted with one response Close.

Completion evidence is committed independently of delivery: cancellation after
successful completion can discard Events while Result preserves the successful
message, matching existing host semantics. No atomic Done-vs-cancel guarantee is
claimed. Errors before completion produce one fixed error/aborted result without
preview content; already shown previews cannot be retracted or treated as success.
No external observer callbacks are installed by this adapter.

## Scenario migration and remaining registration gates

The pinned upstream [text lifecycle scenario](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/test/stream.test.ts#L954-L988)
is exercised through actual host Events: an io.Pipe supplies only the first frame;
the test must receive text_delta before making terminal bytes available. This is
not buffered replay. Original tests also cover late Close failure, unsupported
response kinds, caller input snapshotting, preflight no-call failures, cancellation,
resource closure, Result-first delivery and queue bounds. No upstream code/fixture
copy or live authenticated request is involved.

There is still no default provider/API/model registration, login/refresh/cache,
pricing/capability inference, tool execution or durable raw-history evidence. A
public registration/configuration API must explicitly expose text-only constraints,
executor/no-redirect policy and credential/profile binding; ordinary `Stream`'s
implicit default HTTP-client normalization must not silently opt this path into
network use. Community protocol coverage and real account/service validation remain
separate, unverified gates. This PR does not bypass them.
