# Offline cross-component response coordinator

This draft depends on #54, transitively #52/#51. It connects the existing
framing/envelope, tool assembly, metadata and terminal-policy components over
caller-supplied fixture/cached bytes. It does not connect HTTP, credentials,
provider registration, request DTOs or tool execution. No live calls are made.

## Result and completion contract

- Buffer ordered text/reasoning/completed-tool parts, bounded by explicit limits.
  Text and signature fields in the same reasoning event are both retained, as
  opaque fragments, without claiming signature validation or replay support.
- No callback or partial output exists. Any CRC/protocol/schema/usage/transport
  error, cancellation, refusal or unsupported event returns an entirely zero
  response, including no previously completed tools.
- Read the whole supplied stream. **EOF alone is never completion evidence.**
  Require a valid explicit metadata stop reason and the #52 final decision.
  Only exact `io.EOF` is clean; wrapped/joined EOF remains an error because it
  can accompany a partially read new prelude or another reader failure.
  The reader adapter also retains non-EOF errors independently: io.ReadFull can
  otherwise swallow an error returned alongside an exactly full buffer. A full
  final frame accompanied by a transport error must still fail without output.
- Tools require explicit stop and complete valid object arguments. No EOF flush.
  TOOL_USE must agree with staged tools; MAX_TOKENS discards tools. The returned
  inert calls still require host schema validation and normal authorization.
- After a metadata stopReason appears, only further consistent metadata fields
  may arrive. No later content/tool event is accepted. Metadata-only splits can
  add usage after stopReason, but conflicting terminal reasons fail permanently.

## Strict subset and compatibility gaps

Accepted event names are assistantResponseEvent (string content),
reasoningContentEvent (string text/signature), toolUseEvent and metadataEvent.
Known exceptions terminate with a safe allowlisted name, never the body.
Citation, contextUsageEvent, metering, redacted reasoning and all other events
are currently rejected rather than silently discarded. Extra content fields and
text/reasoning interleaved inside an open tool are also unsupported. Ordinary
events still require application/json content type under #51's explicit subset.

These restrictions may reject normal community-provider responses. Passing
synthetic tests would not establish broad compatibility, service permission or
production readiness. Event vocabulary follows pinned community observations,
not an official Kiro API guarantee. The earlier lowercase `end_turn` synthetic
envelope fixture is not valid completion evidence for this coordinator.

## Resource, cancellation and ownership boundaries

Caller supplies per-message bytes, aggregate payload bytes, aggregate decoded
text/reasoning/signature bytes, event count, tool count and per-tool argument
bytes. Frame/event limits also bound header work; metadata values and aggregate
tool arguments consume the bounded stream input. These are not a precise total
heap limit. No buffering is allowed to grow without an explicit relevant bound.

Context cancellation is checked before/after each underlying read and at final
return. A blocked Read cannot be forcibly interrupted by a pure reader adapter:
the caller must arrange cancellation/closing of that reader. The coordinator
never closes it, starts a goroutine, or retries a failed read. Caller input and
reader ownership must remain exclusive during collection; no concurrent mutation.
Reader error strings are not returned, avoiding accidental secret/body disclosure.

## Validation status

Go test sources now cross the real component boundaries with synthetic binary
frames: fragmented reads, paired reasoning/signature, exact token counts,
tool staging, metadata-only terminal splits, EOF without stop, CRC/truncation,
conflicting/refusal metadata, wrong/duplicate JSON, unsupported events, wrapped
EOF, cancellation, reader ownership, error redaction and all resource budgets.

Static review caught wrapped EOF incorrectly being treated as clean completion;
the implementation now uses exact sentinel comparison and includes regressions.
Independent follow-up review caught io.ReadFull swallowing full-buffer errors;
a sticky reader failure guard and ordinary/wrapped full-read tests now cover it.
This is review evidence only. **Go compile/gofmt/tests/race/fuzz have not run.**
No compiler was installed, no CI dispatched, no credentials or live services used.

```text
gofmt -w pkg/pigo/provider_kiro_offline_response*.go
go test ./pkg/pigo -run TestKiroOffline -count=1
go test -race ./pkg/pigo
go test ./...
```

Do not enable a provider or make live calls until actual Go validation, permitted
identity/auth/endpoint contracts and the documented compatibility gaps are resolved.

Consecutive (0, nil) reads are limited to 100, then sticky io.ErrNoProgress
causes a zero response. Positive reads reset the counter. This does not interrupt
a blocked reader or add a time limit. Regression source added; Go not run.

## Reasoning, usage and ordering: deliberately not a replay adapter

Reasoning parts retain exact event order and each event's text/signature pairing.
A signature-only part is not assigned to another part, signatures are not checked,
and fragments are not flattened into assistant history. There is no evidence here
for safe signed-block reconstruction or for replay of redacted/private reasoning.
The upstream community parser is not a complete official protocol contract.

This subset treats token fields as partial snapshots, not deltas: identical repeats are idempotent,
different repeats are rejected, and disjoint fields merge. Missing counts stay
absent (distinct from reported zero); wire totalTokens is retained without forcing
an invented sum of input/output/cache counts. Credits and capacity percentages
are not converted into billable tokens. This implementation does not aggregate
multiple turns or retries; those require separate trustworthy invocation IDs and
an explicit usage ledger contract.

Usage-only metadata may precede content. Once a stopReason is seen, only consistent
metadata can follow, and a late contradiction rolls back the entire buffered
response including tools. These are conservative subset rules, not claims that
all upstream event orders conform. The result is still inert event evidence:
turning it into a persisted or replayable pi-go assistant is unsupported, notably
when reasoning is present or text follows tools. New cross-component test sources
cover these boundaries; they remain unexecuted without the approved Go toolchain.
