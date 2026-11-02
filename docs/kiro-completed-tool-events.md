# Completed tool lifecycle events (draft)

Depends on #89, not enabled in the text-only live provider adapter.

A pure, buffered event projector validates the entire completed response through
projectKiroCompletedTools before constructing any return value. It never pushes
events, invokes tools or calls a network. No unverified wire tool fragment becomes
a host ToolCall. HTTP EOF/length/Close validation is still the caller's prerequisite.

Returned lifecycle: start, then each text's start/delta/end, each tool's
toolcall_start/delta/end, and one TOOL_USE done. Tool start contains ID/name only;
delta retains the complete original raw argument string, and end contains the
validated parsed+raw ToolCall. These are **replayed completed events**, not
incremental network tool deltas. A full tool call can appear in a partial snapshot
only because whole-response validation has already succeeded.

No future blocks, usage, stop reason or final Message appear in earlier events.
Nested argument maps are cloned independently across event fields and snapshots.
json.Number retains exact numeric evidence; it is not automatically converted
for agent schema validation/execution.

Any unsupported evidence, cancellation or cumulative event-byte budget failure
returns nil, never a partial batch or synthetic success. Context is checked before
projection, at every event addition and before return. CPU work is bounded but not
preempted inside a single strict JSON check or clone. Cancellation can occur after
return: a future publisher must recheck context and implement its own terminal
delivery contract. This function does not solve delivery races.

Maximum 4096 content parts / 128 tools inherited from #89; cumulative conservative
snapshot payload accounting is capped at the host 32 MiB limit. This is not a heap
measurement, and does not guarantee the batch fits the host's 2048 pending-event
queue. A later queue adapter must cap event count/reserve abort capacity. No batch
is blindly fed to a host stream here.

## Test migration

Scenario concepts are independently authored from the pinned community upstream
[stream.test.ts at dd93f2e](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/test/stream.test.ts):
structured tool lifecycle, multiple calls, exact accumulated argument strings and
terminal TOOL_USE. These are synthetic fixtures, not copied upstream tests or
official Kiro captures. Upstream partial realtime tool emission is not claimed.

Three new tests verify exact mixed text/two-tool sequence, content indexes,
big-integer precision, nested-map isolation, complete-only usage, cancellation at
multiple assembly stages (including final return check), failed terminal reasons,
unknown tool names, invalid and mid-batch byte limits. Prior #89 collector-backed
tests establish EOF alone cannot authorize calls.
