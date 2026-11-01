# Envelope classification is not event lifecycle support

This incremental addition depends on the unverified framing foundation in PR
#51. It is internal, unregistered and never called from a live provider path.
It adds no network, auth, tooling, retry or output behavior.

## What is implemented

- Require string `:message-type` and exact `application/json` content type.
- Accept only pinned-community known event names or exception aliases, reject
  conflicting event/exception headers and unsupported kinds/vocabulary.
- Normalize known error members to exception classification even when carried
  in an event frame. Preserve the original name; do not expose error bodies in
  the fixed validation error.
- Check UTF-8, JSON syntax and object root; retain raw bytes without schema
  interpretation. **Duplicate JSON keys and field types are not yet validated.**
- Unit-test sources consume all 18 synthetic frames for envelope classification
  only. Existing corpus expectations about tools/usage/lifecycle are NOT thereby
  implemented or tested.

Exact content type, mandatory headers and fail-closed unknown-event policy are
deliberately conservative assumptions. They may reject legitimate protocol
extensions; adjust only with verified fixtures, not payload-shape guessing.

## Why the complete state machine is deferred

Synthetic frames define desired tests, not captured service behavior. Three
critical contracts remain uncertain:

1. A tool continuation without an ID cannot safely be assigned when more than
   one tool is open. Proposed initial policy: at most one open tool; any ambiguity
   fails rather than guessing or executing.
2. Neither `metadata.stopReason` vocabulary nor EOF alone is verified as a
   universal completion signal. Preserve raw reason; do not invent a successful
   finish. Premature EOF with an unfinished tool must fail.
3. Simultaneous reasoning text/signature and repeated metadata need precise
   retention/accounting rules. Do not drop a field or sum cumulative usage blindly.

Proposed future transitions for review:

| Current state | Input | Intended next state / result |
| --- | --- | --- |
| Initial | Valid text | Streaming; deliver one delta |
| Initial/Streaming | Valid structured tool start, none open | ToolOpen; retain identity; no execution |
| ToolOpen | Valid continuation | ToolOpen; append bounded exact bytes |
| ToolOpen | Stop + complete object arguments | Streaming; publish validated structured call once |
| Any nonterminal | Exception, malformed schema, ambiguous continuation, cancellation | Failed; no replay |
| Streaming | Verified completion contract | Done; final accounting once |
| ToolOpen | EOF | Failed; never publish executable partial arguments |
| Done/Failed | Any input | Reject/ignore without output or retry |

Before implementing transitions, define duplicate-key rejection, tool ID
uniqueness, field schemas, argument/event/stream budgets and completion semantics.
The provider maps validated calls to pi-go events only; agent-layer authorization
and tool execution remain separate.

## Validation status

Go compilation, formatting and all Go test execution remain blocked by the
unavailable approved toolchain. No claim of a working state machine or usable
Kiro provider is made. After approval, run `gofmt`, `go test ./pkg/pigo -run
'TestKiro(EventStream|Envelope)' -count=1`, both existing fuzz targets, race tests
and the full repository suite. Do not enable provider registration based on
static review alone.


## Review corrections: terminal exceptions and compatibility subset

Known service exception names are classified before body/content-type checks.
They return `errKiroEnvelopeException` plus the allowlisted exception name and a
syntax-only `PayloadValid` flag; the body is never retained, even when valid.
This preserves header-only error identity without treating exceptions as
successful events or exposing service error text. Consumers must treat every
non-nil error as terminal, while optionally retaining the safe known name.

Ordinary events still require `:content-type: application/json`. The pinned
community fixture encoder often omits that header, so this is an explicit
compatibility gap, not proof that those upstream frames are malformed. A missing
header rejection test now pins the strict subset. No Go checks have run.
