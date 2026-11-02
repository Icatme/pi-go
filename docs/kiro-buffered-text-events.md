# Buffered Kiro text event projection

This unregistered pure helper extends the completed-text projector with host
`start`, per-block `text_start` / `text_delta` / `text_end`, and exactly one `done`.
It replays only an **already completely collected and validated** response. It is
not real-time streaming, a network executor, a provider factory or model catalog.
No goroutine, callback, credential or tool execution is introduced.

Each offline text part remains a separate block, including empty parts when the
total text is nonempty. Start has no text; text-start opens an empty block; delta
and end contain the text accumulated at that position, never later blocks. Only
Done carries final stop/usage evidence. Its message exactly matches the existing
completed-text projection; absent usage remains unknown, explicit MAX_TOKENS
remains length. Unknown/partial usage, tools, reasoning/signatures, context usage
observations and unsupported stop states still fail closed with no event slice.

All snapshots are detached. Inputs may not be concurrently mutated. Cumulative
conservative `assistantEventBytes` accounting is caller-bounded up to 32 MiB,
checked before cloning each snapshot; this prevents unbounded quadratic retained
snapshot growth. It is not an RSS bound (validated source, slice capacity and
allocator overhead are separate). Maximum 4096 source parts means 12290 events.
A late validation/budget failure returns nil, not an emitted prefix. The host's
actual delivery queue has its own 2048 pending-event and byte limits: obtaining
this slice does **not** authorize pushing it all into a slow stream without a
separate delivery/backpressure design. Cancellation and live partial delivery
remain future adapter work, not behavior simulated by this pure helper.

## Independently authored upstream scenario mapping

Pinned community source: [stream.test.ts L954–988](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/test/stream.test.ts#L954-L988).
Our synthetic-frame test carries over the lifecycle ordering and the invariant
that concatenated text deltas equal completed text. It does not copy fixtures or
source. Unlike upstream, adjacent wire parts are not coalesced into one host block,
and context percentage is not terminal proof. Explicit END_TURN metadata is
required; truncated wire yields no host events. Reasoning lifecycle and realtime
latency are not migrated by this change. Other tests cover UTF-8/repeated text,
empty block boundaries, snapshot ownership, terminal length, absent/reported
usage, unsupported evidence and late budget exhaustion.
