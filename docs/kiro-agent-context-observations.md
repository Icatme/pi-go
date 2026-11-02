# Whitelisted context observations in agent metadata

Depends on #104. The agent pigo bridge now preserves only the fixed
`kiro_context_usage` observation schema in completed assistant messages. No general
provider diagnostic/error/stack/raw-object forwarding is added.

## Exact data contract

`Message.Metadata["kiro_context_usage"]` contains an ordered JSON-compatible array
of fresh maps with `source_event`, numeric `percentage`, and (only for standalone
contextUsageEvent) integer `part_index`. Allowed source strings are precisely
contextUsageEvent and metadataEvent. Percentage is finite float64 in [0,100],
excluding negative zero. Part index is 0..4095. Key counts are exact. Timestamp,
error/stack, credential/scope/identity, arbitrary extra keys and custom marshalers
are not copied. Source tags are provider assertions, not authenticated provenance.

Only Stop/Length/ToolUse terminal messages admit observations. Partials, errors
and aborted messages do not. Known malformed records discard the entire observation
set and replace it with `kiro_context_usage_rejected: true`; they do NOT rewrite
assistant content/stop reason or authorize tools. No partial plausible prefix is
retained. Unknown diagnostic types preserve their old behavior (not forwarded).

Inspection is capped at 4097 diagnostics. If a recognized observation is found
within that inspected prefix and the list is oversized, only the rejection flag
is produced; lists with no recognized item in the bounded prefix are ignored.
Accepted output is separately bounded to 1 MiB serialized JSON. The serializer
only receives newly allocated fixed-key primitive maps, never source values with
custom serialization behavior. All observations remain separate; no token/cost,
compaction action, or billing estimate is inferred.

## Persistence, logs and permission boundaries

Message.Metadata is already JSON-persistent, cloned by the engine/session reducer,
and carried in normal message events. These metrics can therefore appear in
caller event logs or saved session/checkpoint files; this is intentional operational
metadata, NOT a confidential credential channel. No logger is added here.
Only percentages/source/part position cross this boundary; no prompt/token/raw
error content is copied. Editing transcript metadata can still affect existing
whole-message session equality checks, as with other metadata.

The checkpoint approval binding uses definition version, tool ID/name and raw/
canonical arguments, not this metadata. Existing tool schema/permission/context
checks remain mandatory. Observation values have no authority to execute a tool.
The reverse agent→pigo assistant conversion excludes Metadata, so saved metrics
are not replayed into subsequent model prompts or tool arguments.

## Verification and limitations

Four original agent unit tests cover strict schemas, malicious serializers never
called, nonfinite/negative-zero/type/index/extra-key failures, all-or-none rejection,
unknown-provider/no-diagnostic behavior, final-only handling, input/clone ownership,
4097-record/JSON budgets, JSON persistence and model-history exclusion.

A registered Agent integration test receives an actual synthetic contextUsageEvent,
saves/restores its snapshot JSON, continues a text-only session, verifies both
completed observations survive, and verifies neither next request contains those
metadata fields. Agent tool-call replay after session restore uses
[caller-authored history](kiro-agent-authored-compatibility.md).

No real account/service/credentials, automatic transport/network, CI, merge,
deployment or permission relaxation is exercised. Derived IDs and private endpoint
interoperability still require separately authorized real-service validation.
