# Completed context observations as host diagnostics

Depends on #103. The collector already accepts bounded contextUsageEvent data;
the completed text/tool projectors previously rejected those parts. They now
preserve valid observations in AssistantMessage.Diagnostics instead of silently
dropping them, guessing token counts, or rejecting otherwise supported completion.

Each diagnostic has Type `kiro_context_usage`, caller-supplied completion timestamp,
and Details containing source_event plus percentage. For contextUsageEvent it
also contains part_index: the original collector retained-part position, NOT a
raw frame index. Repeated observations remain separate and ordered. A metadata
snapshot tokenUsage.contextUsagePercentage gets source_event `metadataEvent` and no invented index; its
chronological position is unknown and it is appended after part observations.
Timestamp is projection/completion time, not a claimed wire-event timestamp.

Percentage must be finite in [0,100], rejecting negative zero as the wire parser does. Wrong/missing fields on a context part fail
closed. Other unknown parts and partial token usage still reject. The source
response/pointers are unchanged. Diagnostic details are freshly owned, then use
normal detached event/message cloning. No percentage updates Usage, UsageReported,
cost, stop reason, tools, or final-completion requirements. Context-only responses
still cannot become a text answer; missing terminal/EOF/Close still fails.

Serialized diagnostic JSON bytes consume the same bounded completed-projection
payload allowance as text/tools; final event/queue accounting additionally charges
the full diagnostic structure. Original part ceilings remain enforced before
filtering observations. This is conservative validation, not a hard heap limit.
Diagnostics appear on final Done/Result only, never partial previews. Existing
MAX_TOKENS removal of staged tools and tool permission gates are unchanged.

Six original tests cover ordered independent/metadata sources, pointer and
Result/Done isolation, collector→tool queue, malformed/incomplete rejection,
projection/event budgets, public registered text output and truncated responses
releasing no diagnostics. The internal realtime text-only adapter shares this
completed projector: a dedicated positive/late-Close-failure test confirms final
diagnostics without provisional observations or tool events. Its old negative
case now uses invalid101 instead of a supported percentage. This does not register
a new realtime API. Existing metadata negative fixture now uses invalid101
rather than valid50; positive tests explicitly assert retained evidence.

Important boundary: agent/pigo_stream_model.go currently does not translate any
pigo Diagnostics into agent Message metadata. This change guarantees preservation
at the pigo API/queue boundary, NOT durable end-to-end agent/session retention.
A separate explicit bridge is needed. No live service/account test, CI, merge,
deployment, token/cost estimate or execution authorization is implied.
