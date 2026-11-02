# Candidate run-owned evidence scope (not implemented)

This design investigates the ToolCall source-layout blocker recorded in
[the frozen #128 audit](kiro-stack-audit-128.md). No production behavior changes.
The external fixture `agent/testdata/compat_toolcall/toolcall_test.go` intentionally
fails to compile on the current Kiro tree and compiles on the pinned master.
It is an explicit acceptance probe, not a passing test hidden behind a skip:

```
go test -mod=readonly ./agent/testdata/compat_toolcall/toolcall_test.go
```

## Concrete ownership boundary

Engine.RunWithHooks, ContinueWithHooks and ResumePendingToolCallsWithHooks each
already establish per-invocation context state. A candidate private evidence
scope could be freshly allocated at each of those three entries, replace any
inherited scope, and close/erase retained entries on every return. No global map,
new exported struct field, Metadata value, persistence format or public token.
An unexported context key would pass the scope to pigoStreamModel.Stream.
Context access is an internal capability, not authentication or a defense against
arbitrary code already running inside the host process.

The stream adapter must stage raw values privately. Partial events and public
Message/ToolCall values must never carry the private raw evidence. Engine's
existing Close -> Wait -> joined event/wait/close-error validation is the earliest
safe place to commit a completed tool response. Use a private adapter interface,
not a new method on public AssistantStream. No terminal error, cancellation,
malformed role or incomplete assistant may populate the scope. Commit must be
atomic across the full response and must compare the final returned message to
its staged identity/bindings; callbacks cannot substitute an unrelated message.

Replay must require the same live scope and a complete assistant envelope binding
(API/provider/model/response ID, terminal reason, ordered tool positions and typed
per-tool binding), not an ID-only lookup. Timestamp and ThinkingLevel are adjusted
by Engine and cannot be naively treated as immutable wire identity. The existing
typed digest can retain exact raw JSON separately without reserializing parsed
floating-point values. Different raw strings under one identical replay binding
must permanently invalidate that binding within the scope, never overwrite it.

Bound total entries, tools and retained bytes across the run; scope overflow must
fail closed rather than silently evict a historical entry into ambiguous reuse.
Close and concurrent capture/replay require synchronization. Close prevents later
commit/replay even when a callback retains the context. Erasing map references is
not a promise of cryptographic memory zeroization in Go. Avoid raw values in any
errors, fmt, JSON, callback payloads or diagnostics.

## Deliberate limitations requiring acceptance before implementation

- This preserves evidence only for model/tool/model loops within **one Engine
  invocation**. A subsequent Prompt, Continue or resumed suspended tool batch has
  a new scope, even with the same SessionID and an entirely in-memory snapshot.
  Older tool histories then lack original raw evidence and strict Kiro replay
  rejects them. This is a regression relative to the current private-field bridge,
  not an equivalent transparent repair.
- Extending ownership to an Agent/session lifetime would require a separate
  lifecycle design; do not infer it from a SessionID string or reuse caller ctx.
- Within one scope, a fully identical rebuilt value and a legitimate clone are
  indistinguishable. Even a JSON round-trip back into that same scope may recreate
  a matching value. Therefore the design **cannot** promise all deserialized
  values lose replay evidence; it can promise only separate run/scope isolation.
- Direct pigoStreamModel calls without an Engine-owned scope would receive no raw
  replay evidence. Existing standalone conversion/clone tests need revised
  contracts rather than being deleted as if no behavior changed.
- A scope is provenance matching, not tool approval, schema validity or account
  authorization; normal host checks still apply after completed model output.

## Minimum acceptance matrix

1. Explicit external seven-field unkeyed compile probe passes, public ToolCall
   fields restored exactly, and existing JSON bytes unchanged.
2. Same-run two-tool-round fake Agent loop preserves raw large-number literals;
   multiple scopes with identical IDs and SessionIDs remain isolated.
3. Edits to every binding field, different model/provider, reordered tools and
   same binding/different raw all reject replay; identical-clone behavior is
   documented and tested rather than called a forgery detector.
4. No commit after partial, terminal error, late Close error, cancellation or
   malformed role. All return paths close the scope; retained contexts cannot
   recover or add entries afterward. Race tests cover close versus replay/commit.
5. Aggregate byte/entry/tool limits reject atomically and do not overwrite evidence.
6. Subsequent Prompt, Continue, suspension/resume, restored histories and direct
   adapter calls explicitly demonstrate the unsupported boundary without executing
   tools on an incomplete response or inventing original raw arguments.
7. Existing non-Kiro providers and no-scope conversions retain prior ordinary
   argument behavior; no exported API layout, serialization or default network
   behavior changes beyond the explicitly accepted Kiro capability restriction.

Until that behavioral narrowing is accepted and these tests pass, the compatibility
blocker remains. This document is not permission to silently reduce session replay.
