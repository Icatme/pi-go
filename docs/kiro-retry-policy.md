# Offline Kiro failure classification and unsent-retry advice

This internal pure function is not connected to any provider or transport. It
accepts caller-supplied evidence, returns a bounded category and one boolean,
and performs no I/O, sleeping, refresh, account fallback, tool execution or retry.
It is stacked on the #61 integration workbench a0b68355, independently of request
envelope work. No existing behavior changes.

## Evidence and deliberate differences

Pinned community sources:
- [reason codes](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/retry.ts#L129-L159)
- [modeled exceptions](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/event-parser.ts#L61-L97)
- [runtime recovery](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/stream.ts#L1053-L1171)

The source recognizes quota, capacity, request-window throttle, size and malformed
body reasons, and locally retries certain service failures. This implementation
does NOT adopt those retries. A transient classification does not prove an
attempt was unbilled, idempotent or safe to replay.

Only an unambiguous local transport failure with affirmative transport proof
that no request was dispatched may return RetryUnsent. Unknown/sent dispatch,
any observed output, executed tool, cancellation, protocol failure, contradictory
service evidence, missing opt-in or exhausted/invalid budget forbid it.
A timeout, EOF, socket reset, HTTP 429/503 or service exception is NOT proof of
NotSent. A future transport must establish this evidence without guessing.

Cancellation then protocol failure take precedence. Invalid HTTP statuses fail
closed. Exact recognized reason takes diagnostic precedence over exception/status
(e.g. capacity can accompany HTTP 403); an unknown nonempty reason or exception
does not fall through to a more optimistic status classification. Auth is a
diagnostic, never an instruction to refresh or switch accounts. No raw body,
provider prose or exception message is retained. The prose-only upstream phrase
"Input is too long" is deliberately unsupported as a reason code.
HTTP and exception categories are hints, not account-violation claims.

## Inputs and ownership

Status denotes a non-success HTTP status or zero, not the enclosing stream HTTP
status. An exception within an HTTP-200 event stream must pass Status=0; passing
200 deliberately returns unknown. Existing offline exceptions retain only their
safe Name and can therefore use zero. This mapping requires an explicit adapter.

A separately validated, bounded parser must extract Reason/Exception. This
function does not validate wire JSON or accept arbitrary payloads. RemainingAttempts
is a caller-owned budget: this function does not decrement it. A caller acting
on RetryUnsent must reserve/consume a retry atomically, enforce deadlines and
obtain required service-use authorization. Do not wire this into an automatic
retry loop merely because the pure decision returned true.

Retry-After parsing, waits/backoff, per-attempt accounting, request identity,
credential refresh, emitted-output rollback and provider registration are absent.
Never replay an already dispatched generation on this policy.

## Verification status

Unit test sources cover exact reason/exception mappings, nonmatching strings,
invalid statuses, cancellation/protocol priority and every retry guard.
No Go compiler, gofmt, unit/race/fuzz tests have run. Static review is not
execution. No credentials, real endpoint, extra-credit job, CI dispatch or
installation was used. Keep draft pending approved Go tooling.

Suggested checks:
- gofmt -w pkg/pigo/provider_kiro_retry_policy*.go
- go test ./pkg/pigo -run TestKiroFailure -count=1
- go test -race ./pkg/pigo
- go test ./...
