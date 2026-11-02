# Incremental text → completed tool events (pure bridge)

The existing text mapper gains an internal `completeTools` path. This does not
change any HTTP worker, buffered registration or live-text registration yet.

## State and evidence

- `accept` still emits text preview only, with no tool evidence.
- The caller must supply whole-response evidence only AFTER HTTP EOF, declared
  length, cancellation and body Close succeed. This pure helper cannot prove the
  provenance of a manually constructed response.
- `completeTools` uses the existing strict completed-tool projector: explicit
  TOOL_USE, allowlisted names, valid unique IDs, exact raw/parsed JSON evidence,
  supported usage and text-before-tools order. No new protocol acceptance.
- Final text must equal exactly the previewed concatenation, including presence
  of empty text blocks. Leading fragments become the same single host text block.
- The final batch closes text (without replaying Start/TextDelta), adds tool
  lifecycle events and Done. Tool-only responses start without invented text.
- Each snapshot is cost-checked and cloned while staging. No events escape until
  the complete batch fits the remaining cumulative budget. Failures are sticky,
  clear text state and can emit only the existing empty error terminal.

The caller must recheck cancellation while publishing; tool execution still
requires Agent permissions, schema checks and current context. Previously delivered
previews cannot be revoked. There is no tool execution, queue, transport, retry or
registration in this change. It is one building block, not a usable unified live
provider.

## Original scenario tests

Derived independently from community pi-provider-kiro
`dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba` text/tool and truncated-stream scenarios,
using local synthetic response objects rather than copied fixtures:

- split text previews, two tools, no duplicate text, exact large JSON integer,
  no future arguments in ToolCallStart and detached snapshots;
- tool-only terminal without invented text;
- mismatched/missing preview, interleaving, unknown tool, wrong terminal,
  insufficient budget, partial usage and cancellation return no final batch;
- deterministic cancellation at actual batch checks and terminal idempotence.

An initial test incorrectly scheduled cancellation after the function's last
context check, then cancelled only after return. It was corrected to cancel at
an actual final-batch check; no production acceptance was loosened.

The bridge also reserves the combined text-fragment/tool-delta count against the
host queue's 1024-delta limit. Exactly 1023 previews plus one tool is accepted;
1024 previews plus one tool is rejected rather than relying on consumer timing or
silently dropping old previews. Byte accounting remains cumulative across preview
and final batches. Final batch length also stays below the 2048 pending-event cap
with the existing 128-tool ceiling.
