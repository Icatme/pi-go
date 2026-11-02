# Incremental text → host event state machine

This single-owner, unregistered mapper converts provisional text callbacks into
host event values immediately. It is not a transport, delivery queue, goroutine,
provider registry or credentials/configuration factory. Inputs/state must not be
mutated concurrently. Model identity/time are explicit caller metadata; the same
identity validator is shared with the existing completed-text projection.

- `accept(text)` returns start/text_start/text_delta for the first fragment and
  one text_delta thereafter. All deltas target content index0 with cumulative text
  in detached Partial snapshots. Empty fragments still consume fragment allowance.
- `complete(finalEvidence)` validates the entire existing strict text-only contract
  (tools/reasoning/context usage/partial usage/invalid stop still reject), compares
  concatenated final text with observed text, then atomically returns text_end/done.
  Adjacent fragments coalesce into one host block, so different fragment boundaries
  with identical accumulated text are allowed. This differs intentionally from
  the earlier buffered projector's per-fragment blocks. Only Done carries final
  usage and stop evidence; absent usage remains unknown and MAX_TOKENS stays length.
- Any rejected accept/complete is sticky and returns no partial batch. Further
  accept/complete cannot recover into success. The caller must then call fail.
- `fail(cancelled)` emits at most one fixed error/aborted terminal, even before any
  text or after rejection; after Done or a prior fail it is inert. Previously
  emitted previews cannot be retracted. Failure terminals contain no preview or
  caller/transport error text. No synthetic success or executable tool event exists.

Every returned snapshot is detached. Whole batches are costed before state/budget
commit; failure does not publish a half start/delta or end/done batch. Internal
retained text is cleared on failure/terminal. Cumulative event payload accounting
(including already returned snapshots) is caller-bounded to at most32MiB, with a
fixed error terminal reserved up front. Text is limited to1MiB and fragments to1024;
independent byte limits may reject earlier, including with empty fragments. These
are conservative payload/work limits, not process RSS guarantees. No claim is made
that returned events can be blindly pushed into a host queue without separate
lifecycle/backpressure handling; this step only constructs event values.

## Tests and upstream mapping

The pinned community [text lifecycle scenario](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/test/stream.test.ts#L954-L988)
checks start/text_start/deltas/text_end/done and accumulated text. Independently
written tests now cover this at the host-value boundary, including coalescing,
snapshot isolation, exact complete evidence, unknown/reported usage and length.
Additional regressions cover sticky unsupported-final/text mismatch, batch budget
atomicity/error reserve, empty-fragment limits, early/repeated failure, invalid
identity/UTF8/text limits and all-empty final rejection.

A fake injected HTTP attempt supplies a gated body: host delta creation must occur
before the callback unlocks the later wire segment. A late Close error produces
Error instead of Done despite the earlier valid delta. This proves incremental
HTTP-observer→host-value composition offline, not live-service latency or provider
registration. Completion must only be called after the full injected attempt,
including response cleanup, succeeds; collector success before HTTP Close is not
sufficient. No original upstream code/fixtures or real credentials are used.
