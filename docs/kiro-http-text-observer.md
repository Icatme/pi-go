# Injected HTTP boundary with incremental text previews

`runKiroInjectedAttemptWithTextObserver` connects the validated incremental text
collector to the existing explicit one-attempt HTTP execution function. The old
`runKiroInjectedAttempt` supplies a nil observer and preserves its prior behavior.
No default HTTP client, retry, redirect, refresh, credentials discovery, provider
registration or live request is added.

The caller supplies the same explicit executor, identity, token, limits and context.
HTTP status/content type/encoding/length admissibility is checked before any body
read or preview. Before each callback the wrapper also checks that the completed
frame has not consumed the extra EOF-probe byte or exceeded declared Content-Length;
these transport bounds cannot be bypassed by a frame ending at the probe byte.
Each preview is a validated assistant text frame, delivered before
the next frame is read. A gated response-body test requires the callback to unlock
the tail and checks that response Close has not yet occurred. This demonstrates
incremental behavior through the injected HTTP boundary, not a real network latency
measurement or a registered host provider.

**Previews do not authorize completion.** A later frame/checksum error, truncation,
Content-Length mismatch, wire-limit failure, cancellation or response Close error
still clears final evidence. A Close failure after all previews cannot retract what
a UI displayed; consumers must show final failure separately. Tools remain staged
until complete collector validation, and a later HTTP failure still discards them.
Callbacks receive no credentials, request/header objects, reasoning or tool data.

Callback errors/cancellation immediately stop further body reads, then run the same
response cleanup. Error text from callback/transport/Close is not returned. The
executor is invoked exactly once, and no retry is performed after partial output.
Response+error still closes the provided body without reading or previewing it.

The executor, reader, Close and callback are trusted cooperative boundaries: they
must honor context and must not independently redirect/retry/block forever. The
wrapper cannot preempt them and does not recover callback panics or spawn workers.
It never changes their permission to call external services; tests use only fake
injected responses and synthetic bytes, with no real credentials or network.

## Test migration and verification scope

This joins the pinned community text-delta scenario already mapped in
[the incremental collector document](kiro-incremental-text-observer.md) to an injected
HTTP response, preserving exact final evidence versus the nil-observer path.
Seven original Go test functions add pre-tail/pre-Close delivery, late transport
completion failures, header/status rejection before previews, fixed-error cleanup,
callback cancellation, exactly-one executor invocation and nil-executor denial.
They are not claimed as upstream retry/auth-discovery/real-service tests. Existing
single-attempt framing/ownership/budget tests continue to run unchanged.

Host event mapping/registry and a safe configuration surface remain separate work.
This API returns completed evidence plus provisional callbacks, not a ready-to-use
registered model provider or a promise of full community protocol compatibility.
