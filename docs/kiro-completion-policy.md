# Offline terminal decision policy

`decideKiroCompletion` is a pure final safety decision over caller-supplied
evidence. It is not a streaming state machine, frame parser or provider. No
production path calls it. Its presence does not make Kiro usable.

Community evidence at the pinned revision lists uppercase metadata stop reasons
in [stream.ts L1712-L1750](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/stream.ts#L1712-L1750).
The same source heuristically falls back from missing stop reasons using emitted
tools/context usage. This stricter policy intentionally does not adopt that
heuristic or claim it is an official service guarantee.

| Evidence after whole-stream consumption | Decision | Staged tools |
| --- | --- | --- |
| Cancellation | aborted | suppress |
| Transport/protocol/event failure | error | suppress |
| Stream not ended, negative tool count | error | suppress |
| CONTENT_FILTERED | refusal error | suppress |
| MODEL_CONTEXT_WINDOW_EXCEEDED | context error | suppress |
| PAUSE_TURN | unsupported pause error | suppress; no auto-continuation |
| Any unfinished tool | incomplete error | suppress |
| END_TURN and no tools | stop | none |
| END_TURN with staged tools | inconsistent evidence error | suppress |
| MAX_TOKENS with no unfinished tool | length | suppress even complete tools |
| TOOL_USE with completed tools and none open | toolUse | eligible for host authorization only |
| TOOL_USE without completed tools, missing/unknown/lowercase reason | error | suppress |

The caller must stage tools until the entire stream has been consumed and this
decision has passed. `ReleaseTools` is not authorization to execute: the normal
host schema validation, policy, confirmation and agent execution boundary still
apply. Partial text may already have been shown, but errors never trigger replay.

## Required future evidence collector

- Classify every frame and validate schemas; only count fully validated tools.
- Track transport errors, cancellation and unfinished argument state separately.
- Reject contradictory/repeated terminal metadata rather than letting a later
  END_TURN overwrite CONTENT_FILTERED or any earlier error.
- Require a verified end of stream; metadata alone does not mean later refusal
  or transport failure cannot arrive. EOF alone is also insufficient.
- Keep raw wire stop reasons; do not silently normalize case. Earlier synthetic
  `end_turn` examples are envelope fixtures, not valid completion evidence.
- Do not use context percentage, token estimates or mere content presence as a
  completion signal. Do not expose internal evidence objects to untrusted callers.

## Validation

Unit-test and fuzz sources cover all supported reasons, suppression on failures,
missing/unknown metadata and the invariant that only the strict TOOL_USE path
can release tools. Go compilation, gofmt, tests, fuzz and race checks have **not
run**. This is a static-reviewed policy candidate, not tested runtime behavior.
Before merge, run the approved toolchain checks for #51/#52 and:

```text
gofmt -w pkg/pigo/provider_kiro_completion*.go
go test ./pkg/pigo -run TestKiroCompletion -count=1
go test ./pkg/pigo -run '^$' -fuzz '^FuzzKiroCompletionNeverReleasesOnFailure$' -fuzztime=30s
```
