# Upstream contract guards, 2026-11-14

Based on fetched master `9a8e190` after #223. This slice adds regression tests/documentation, not a claim of new runtime bug fixes. It does not duplicate Mistral #224 or DeepSeek #225.

## Already-correct Go behavior made explicit

- [Upstream #11226](https://github.com/earendil-works/pi/issues/11226): Code Mode's script wrapper is a zero-argument function, so lexical `const tools` and `const console` work. The new real-WASM sandbox regression checks discovery through a local `tools` binding, local `console`, then normal global tool dispatch in a later isolated run. A temporary Go overlay that adds the two wrapper parameters makes the test fail with the expected duplicate-parameter syntax error. No runtime change is needed.
- [Upstream #9134](https://github.com/earendil-works/pi/issues/9134): Go forwards the whole tool schema into the Anthropic request, not only type/properties/required. New serialized-request tests preserve root anyOf/allOf/additionalProperties under API-key and OAuth request shaping, without changing the caller schema. A temporary overlay that flattens to the three legacy fields makes the regression fail. This proves local serialization only, not Anthropic's acceptance of every JSON Schema keyword.
- [Upstream #10984](https://github.com/earendil-works/pi/issues/10984): generic Go transforms already replace non-vision images with visible placeholders while preserving original input. Existing tests cover the boundary. CommandCode intentionally rejects direct non-vision user images under its own documented contract; no global silent-dropping rule is introduced.
- [Upstream #10844](https://github.com/earendil-works/pi/issues/10844): Go does not expose the reported TypeScript `streamProxy` path. This does not prove every adapter's tail-error behavior identical; no speculative terminal-event rewrite was made.

## Host-owned command ordering

[Upstream #11950](https://github.com/earendil-works/pi/issues/11950) also motivated an offline Agent+exectools check. Default concurrent command/Python and following-read fixtures can read before execution ends. The existing `ToolExecutionSequential` setting prevents this in a normal Agent batch; both sequential controls passed race. Since Go factories wrap arbitrary host functions rather than a built-in shell, this slice documents the host choice instead of changing throughput globally or hard-coding tool names. Whole-batch sequential behavior is distinct from the proposed upstream segmented scheduling and from Code Mode Promise ordering. These temporary audit fixtures are not a real shell or remote-job acceptance test.

## Validation boundary

Remote Linux Go 1.26.11, offline fixtures. Focused race tests pass; both injected regression controls fail as intended. Full affected-package race/vet results are recorded in the PR. No real providers, paid models, external application acceptance, merge, release, CI dispatch or deployment.
