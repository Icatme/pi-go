# Offline integration contract candidates

`synthetic-wire.json` contains 12 hand-authored cases / 18 AWS event-stream
frames. No captured traffic, real token, account, customer text or tool execution
is included. Payload vocabulary was reviewed against community
[`src/event-parser.ts`](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/event-parser.ts)
at v0.12.3. This is not an official Kiro service contract or evidence that a
service will accept requests. The fixture strings and wire encoder are original.

Each frame stores string headers, UTF-8 payload, independently generated wire
hex, and a classification hint. Each case records intended **future pi-go**
semantics, not verified behavior of either implementation. Do not mark these
expectations passed until an event-layer test consumes them. Some deliberately
differ from the community provider for fail-closed behavior.

## Differences and test gates

| Area | Community reference observed at pinned revision | pi-go integration contract to validate |
| --- | --- | --- |
| Framing | Smithy marshaller in stream.ts | #51 is internal framing only; run Go tests before further production integration |
| Event dispatch | Known event keys; unknown keys may use payload-shape fallback | Route by typed headers; unknown payload must never manufacture tool calls |
| Tools | Structured fragments plus prose/tag fallback parsers | Structured events only initially; prose examples stay text; orphan/incomplete fragments fail |
| Reasoning | Typed reasoning events; parser chooses text before signature | Preserve both fields when simultaneous; keep separate from visible text/history |
| Usage | Metadata tokenUsage separated from metering credits | Validate finite/non-negative numeric values; never count credits as tokens; avoid duplicate usage sums |
| Retry | Request/stream retry helpers, bounded service-delay handling | No replay after any visible output; no monthly-quota retry; cancellation interrupts backoff |
| History | Tool-result relocation, merging and validation helpers | Preserve user chronology or reject unsupported shape explicitly; no invented user text or silently dropped result |
| Auth/models | CLI/IDE accessors and refresh/catalog lifecycle | Explicit injected credentials only; no local-store scans or cross-account fallback; catalogs account-scoped |

Other reviewed source files at the same revision: `src/tool-use-parser.ts`,
`src/transform.ts`, `src/retry.ts`. These observations are not a complete audit.
No source implementation was copied; license verification is still a gate for
future upstream code reuse.

## Test sequence before registration

1. Feed each `wire_hex` into the #51 decoder at every possible single split and
   in one-byte chunks. Assert exact headers/payload and deterministic EOF. Test
   each valid frame with prelude/message CRC corruption and every truncated
   prefix. Wire validity does not imply JSON/event validity.
2. Build an isolated event-layer test (no network), consuming the declared
   expectations: text, fragmented tools, orphan/incomplete tools, unknown events,
   quoted tools, credits, exception aliases, malformed JSON and wrong types.
3. Add total-stream/event/tool-argument budgets, duplicate tool IDs, interleaved
   fragment ambiguity, duplicate JSON keys, invalid UTF-8 payload, nested/deep JSON,
   empty stream, absent metadata and EOF-without-tool-stop cases.
4. Use a fake transport and fake clock for cancellation before headers, blocked
   reads, first-event timeout, repeated 429/503, Retry-After, and mid-output error.
   Check closure, no goroutine leak, bounded retries and zero replay. No live
   listener or endpoint is required: inject a RoundTripper and io.ReadCloser.
5. Inject two synthetic identities and assert no token/profile/catalog crossover;
   expired credentials yield a typed error, no subprocess or file access. Assert
   auth headers never follow redirects to another origin and logs remain redacted.
6. Run full pi-go/race tests and a separately authorized live smoke test only
   after allowed endpoint/auth usage is established. Keep registration off until
   all gates are met.

## Validation performed

Python standard-library validation on 2026-11-01 checked all 18 frames' total
lengths, both CRCs, decoded string headers and byte-exact payload equality.
This is fixture validation **only**: Go test consumption and event-state-machine
tests have not run, and production behavior is unchanged.
