# Completed text-only host message projection

This draft implements only stage A of the #73 design: a pure, buffered converter
from a validated offline response to AssistantMessage. It does not construct an
AssistantMessageEventStream, register a provider, emit tool calls, send HTTP or
change agent behavior. It is based on #66 so context-usage observations are
recognized and explicitly rejected rather than accidentally discarded.

## Strict supported subset

- Accept stop + END_TURN or length + MAX_TOKENS only; require both normalized
  and metadata terminal evidence to agree. No success from a zero/error/aborted
  response or missing terminal evidence.
- Preserve ordered text blocks exactly, without trimming, joining or synthetic
  padding. Empty total text is rejected. At most 4096 parts and caller-bounded
  aggregate text up to 1 MiB are admitted; UTF-8 must be valid.
- Reject tool/reasoning/context_usage/unknown kinds and any tool pointer, signature
  or percentage field attached to text. No tools or signed output are produced.
- Model/API/provider identity and timestamp are explicit caller inputs, not
  server-verified observations or automatically generated values. ResponseModel,
  ResponseID, thinking metadata and unobserved diagnostics are not invented.
- Output Content is independently allocated. Caller inputs must not mutate
  concurrently; string copying is not secret zeroization or a hard heap limit.

## Usage policy

Host Usage has int fields but no per-field presence. Therefore this converter
accepts either no token fields (UsageReported=false), or all five recognized
counts (UsageReported=true, including all-zero reported counts). Partial maps,
unknown/replaced fields, negative counts and int64-to-host-int overflow fail with
a zero AssistantMessage. A 64-bit value can be representable on a 64-bit build
but rejected on a 32-bit build; tests contain architecture-aware assertions.

Map uncachedInputTokens/outputTokens/cacheReadInputTokens/cacheWriteInputTokens/
totalTokens directly. Preserve the reported total even when it differs from the
sum; no recomputation, float conversion or arithmetic overflow from addition.
Context percentages and normalized usage evidence are rejected, not flattened
into token counts. Costs remain unestimated zero values, which mean **unknown**,
not free service or verified zero credits. This policy intentionally supports
less than all valid upstream responses until the host presence contract expands.

All errors use one generic message and return a zero AssistantMessage. This
converter trusts that the caller selected evidence from the correct request;
it does not establish request-response/account correlation or authenticity.

## Validation and remaining gates

Go test sources cover block order/model/time, absent vs all-zero counts,
inconsistent totals, exact large integers and 32/64-bit conversion, malformed /
partial metadata, unsupported kinds/signatures/percentages, ownership, invalid
identity and text/block budgets. They do not run any service or event dispatcher.
Go compilation, gofmt, unit and race tests have NOT run because a compatible
approved Go toolchain is unavailable. No installation, CI, builtin registration,
merge-to-master or deployment occurred.

Next: run TestKiroCompletedText, all Kiro tests and supported-architecture/race
checks before adding a buffered event adapter. Event terminal/Result/Close,
observer safety, explicit executor provenance, tool raw-history and reasoning
replay remain the independent gates described in docs/kiro-host-adapter-plan.md
(on documentation PR #73, not inherited here). This is not usable live Kiro.
