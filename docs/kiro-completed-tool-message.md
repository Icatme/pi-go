# Completed tool-message projection (draft)

Based on #88; internal pure adapter only. This does not register a provider or
enable the text-only live stream to execute/emit tool calls.

## Boundary

The caller must supply the collector's whole-response evidence **after** the
HTTP attempt's status, framing, explicit terminal, EOF, length and response Close
checks have all succeeded. A plain Go evidence struct is not a security token;
the projector cannot prove its provenance. No callback/provisional tool output,
network access, execution, schema validation or agent permission grant occurs.

Only explicit matching TOOL_USE terminal evidence is accepted. Original text /
tool order is preserved. Text after the first tool (including empty text) is
rejected because current request history cannot replay it without reordering. Every tool must have a unique native ID and an exact
name in the explicitly supplied request tool-name list. Empty / duplicate /
invalid allowlist entries fail. All-or-nothing zero result on any bad part.

Arguments are independently checked with the strict object decoder, then parsed
with json.Number. Original RawArguments is copied, not reconstructed. Bounded
dual-evidence matching checks the generated parsed representation for compatibility
with the existing history projector. json.Number is deliberate: a downstream
executor requiring float64 needs an explicit, validated adapter, never a lossy
conversion. RawArguments still does not survive ordinary history persistence.

Limits: 4096 parts, 128 declared/returned tools, 1 MiB combined decoded text +
tool IDs/names/raw arguments; strict argument comparison additionally bounds depth,
values and numeric literals. This is a decoded-data budget, not total heap bytes.
Unknown reasoning, signatures, context observations, partial usage and unsupported
metadata are rejected, not silently removed. Complete or wholly absent usage
uses the same extracted helper as the existing text projector. Cost is unknown.

Inputs are single-owner/non-concurrently-mutated. Output maps and argument bytes
are detached. No existing live stream behavior is broadened.

## Independently authored upstream scenario mapping

Community source pinned at
[mikeyobrien/pi-provider-kiro dd93f2e](https://github.com/mikeyobrien/pi-provider-kiro/tree/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba).

- stream.test.ts structured tool lifecycle/TOOL_USE: synthetic complete frame +
  terminal projects an exact ID; EOF-only input cannot create a ToolCall.
- transform.test.ts adjacent tool call/result history: received precise integer
  arguments project through host ToolCall into next-turn wire history unchanged.
- Multiple calls, unexpected names, duplicate IDs, malformed arguments and budget
  boundaries are independently written strict-subset cases, not copied fixtures.
- Upstream's implicit EOF flush, repairs, parsed-only replay and partial realtime
  tool events are intentionally not imported.

This change has no host tool_start/delta/end delivery yet. That lifecycle and
the agent's schema/permission/execution boundary require a separate small change.

## Later context-observation extension

The original context-percentage rejection is superseded by
`kiro-completed-context-diagnostics.md`: valid completed observations are retained
as bounded final Diagnostics, without token/cost inference or relaxed termination.
