# Kiro host adapter / registration gates (design only)

This is a source-backed plan, not a provider implementation or completed rollout.
Baseline host: master `afaebd607e1f6ed0758fc5cf830215d304b063e5`.
Offline attempt workbench: #72 `33fd44a40956cf1bab6ea84f7d250b141067a9ce`.
The separately joined #70 includes additional context-usage/projection/retry-hint
work which #72 does not yet include. Do not blindly treat those branches as one
compiled product. No registry, builtin module or model catalog changes here.

## Verified host contracts

| Surface | Source at the pinned host baseline | Consequence |
| --- | --- | --- |
| API dispatch | pkg/pigo/api_registry.go: APIStreamFunc/APIModule | Must return AssistantMessageEventStream, not kiroOfflineResponse. |
| Provider metadata/auth | pkg/pigo/provider_registry.go: ProviderModule/ProviderAuth | Model metadata, capabilities and credential resolution are separate from API event dispatch. |
| Default client injection | pkg/pigo/stream.go: Stream/StreamSimple; provider_http.go: providerHTTPClient and defaultProviderHTTPTransport | Both dispatchers normalize HTTPClient before the API module; nil can become a real default transport using ProxyFromEnvironment. Non-nil HTTPClient is NOT evidence of explicit caller injection. |
| Event terminal lifecycle | pkg/pigo/types_event.go, stream.go and stream_lifecycle.go | Start/text/tool/thinking events, final Result, Close, cancellation and queue ownership must remain consistent. |
| Usage | pkg/pigo/types_model.go: Usage | Host counts are int, while Kiro evidence is nonnegative int64 and may omit fields. Conversion needs overflow and missing-field policy. |
| Tool evidence | pkg/pigo/types_content.go: ToolCall | Arguments is a map; RawArguments is ephemeral json:"-". Normal persistence cannot preserve exact raw wire evidence. |
| Thinking | pkg/pigo/types_content.go: ThinkingContent | One ThinkingSignature field is not a license to assign isolated signature fragments to guessed reasoning text. |
| Observer callbacks | pkg/pigo/stream.go: startRequest/push/finish | Never pass a token-bearing *http.Request or header map as an observer payload/error. Observer completion/error accounting needs its own tests. |

Links:
- https://github.com/Icatme/pi-go/blob/afaebd607e1f6ed0758fc5cf830215d304b063e5/pkg/pigo/stream.go
- https://github.com/Icatme/pi-go/blob/afaebd607e1f6ed0758fc5cf830215d304b063e5/pkg/pigo/provider_http.go#L73-L95
- https://github.com/Icatme/pi-go/blob/afaebd607e1f6ed0758fc5cf830215d304b063e5/pkg/pigo/api_registry.go
- https://github.com/Icatme/pi-go/blob/afaebd607e1f6ed0758fc5cf830215d304b063e5/pkg/pigo/provider_registry.go
- https://github.com/Icatme/pi-go/blob/afaebd607e1f6ed0758fc5cf830215d304b063e5/pkg/pigo/types_content.go
- https://github.com/Icatme/pi-go/blob/afaebd607e1f6ed0758fc5cf830215d304b063e5/pkg/pigo/types_model.go

## Proposed staged adapter, not yet implemented

### A. Pure completed-text projection

First implement a private pure converter, independent of registration/transport.
Only accept already validated completed text parts with stop/length terminal
state. Reject tools, thinking/signature fragments, context_usage and unknown
kinds until each has a lossless host representation. Never silently discard them.
Do not turn an error/aborted/zero response into a successful empty assistant.

Use explicit caller-selected model identity; do not guess server response model,
response ID, price, context size or capability. Keep timestamps injectable for
repeatable fixtures rather than adding hidden clocks to pure conversion.

Usage must preserve reported evidence: check int64-to-int round trip, reject
negative/unknown fields, and do not sum/recompute totalTokens or convert context
percentages/credits into tokens. Proposed first strict subset: accept no counts
as UsageReported=false, or all five count fields as reported; reject partial
counts instead of silently converting missing values to known zeros. This policy
needs host-contract review; it is not yet adopted in production. No model cost
estimates should be invented from an unrelated provider's catalog.

### B. Buffered event adapter

Only after A has executed tests, map successful validated output into an explicit
start/text_start/text_delta/text_end/done sequence, with final Result equivalent
to the done message. This is buffered delivery after complete stream validation,
NOT low-latency live token streaming. Do not emit tools before final success.
Error/abort must have exactly one compatible terminal event and final message;
late cancellation, observer failures, queue overflow, early Close and repeated
Close must follow the existing managed stream lifecycle, without leaked workers.

### C. Explicit configuration and opt-in factory

Before registration, define how an explicitly supplied executor/credential kind,
account/profile/region and client identity reach the module without confusing
host-default HTTPClient injection with permission to use network. A dedicated
factory/configuration provenance boundary is preferable to detecting nil after
normalization. No environment fallback or implicit credential discovery.
Do not weaken generic providers' existing defaults merely for this module.

Review generic Headers/options/model overrides: unsupported sampling, images,
reasoning, tools, response format or replay features must reject, not be silently
ignored or falsely advertised. Initial catalog must contain only verified,
explicit models/capabilities; no speculative prices or discovery calls at init.

### D. Tool/reasoning replay separately

Current strict Kiro history requires original RawArguments and Arguments=nil,
whereas normal provider calls often supply both and persistence omits raw text.
Therefore publishing toolUse to the normal agent loop is not ready: define raw
provenance, edits, argument-number semantics and persistence before claiming
multi-turn support. Do not clear maps or reconstruct original JSON from floats.
Signed reasoning needs proven association and replay semantics before mapping to
ThinkingSignature. context_usage remains observation metadata, not text/billing.

## Minimum executable acceptance before registration

| Test group | Required assertions |
| --- | --- |
| Pure text | Stable content order, exact identity, supported terminal only, no unknown-field loss. |
| Usage | Missing vs zero, all five fields, inconsistent total retained, MaxInt/overflow on supported architectures, no invented cost. |
| Event lifecycle | Exactly one terminal, done/Result agreement, Close/cancel/overflow/observer paths, no leaked goroutines or partial tools. |
| Explicit injection | Calling through Stream/StreamSimple with no explicitly configured executor never reaches host-default network transport. |
| Secret handling | Fake sentinel absent from observer payloads/errors/URL/body; request/header dumps prohibited. |
| Replay | Tool arguments/raw evidence and editing/persistence round trips, or explicit rejection; no premature tool capability. |
| Isolation | No builtin registration/model advertised until gates pass; test registries restored, no production global side effects. |

Go compile/gofmt/unit/race and broad integration have NOT run in the current
environment; `/usr/bin/go` is unrelated and no installation was authorized.
The minimum unblock is an approved compatible Go toolchain in the remote
environment, then focused Kiro tests, race and repository suite on an explicitly
reviewed joined branch. Until then keep implementation drafts unregistered and
unmerged; this document is not test evidence or live-service authorization.
