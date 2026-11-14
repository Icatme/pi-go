# Explicit retained-thinking projection

`agent/session.State.ProjectContext` and `Session.ProjectContext` provide an
opt-in model-facing projection for **host-generated** compaction summaries.
`Context`, `PrepareRun`, journals, and custom `StreamModel` implementations keep
their existing behavior. This does not rewrite persisted history.

Anthropic documents that a caller-generated summary changes the prefix of
thinking in retained turns on models that perform prefix checks. API-generated
compaction blocks have a different preservation contract. See
[compaction and preserved thinking](https://platform.claude.com/docs/en/build-with-claude/compaction-thinking-blocks)
and [prefix checks](https://platform.claude.com/docs/en/build-with-claude/preserved-thinking/prefix-checks).

After resolving the actual target model, a host which knows that target supports
this prefix-bound thinking behavior can explicitly request:

```go
view, err := s.ProjectContext(session.MainLane, session.ContextProjectionOptions{
    RetainedThinking: session.DropPrefixBoundAnthropicThinking,
    TargetAPI:        "anthropic-messages",
    TargetProvider:   resolvedProvider,
    TargetModel:      resolvedModelID,
})
```

Use `view` only to construct the model request. As with `Context`, `Summary` is
separate from `Messages`; include it as conversation data, not as system authority.
Do not replace audit history or a runtime journal's prefix with this view.
Re-evaluate the choice after a dynamic model switch. There is no automatic model
name detection, registered capability change, or static definition binding.

The zero policy returns the ordinary detached context. The explicit policy:

- Removes whole `thinking` parts, including redacted/signature-only parts, only
  from exact API/provider/model matching assistants in the newest compaction's
  retained tail on the selected branch.
- Leaves subsequent messages untouched, including newly generated thinking.
- Preserves tool calls, their arguments and signatures, and tool results in
  their original order. It does not invent signatures or substitute text.
- Rejects incomplete source identity, unknown parts in an affected assistant,
  and an affected assistant which would have no nonblank text or tool calls.
  Images are retained but do not count as usable assistant content for the
  Anthropic wire converter. `errors.Is(err, session.ErrContextProjection)` identifies this
  failure; the caller must not send a partial projection.
- Leaves fully identified other models/providers/APIs unchanged. Their thinking
  validity is **not** guaranteed by this narrowly scoped projection. It is not a
  general cross-model signature migration or arbitrary-history repair tool.

No beta header or `thinking.block_binding` body setting is added. This is not
the separate typed binding-control capability discussed in upstream issues
11610/10954, and it does not implement native API compaction. There is no
automatic retry, billing adjustment, or live-provider validation. Thinking-only
turns may require a host-chosen recovery strategy; this helper fails rather than
silently deleting a turn or manufacturing a placeholder.

The change is separate from the PR263–290 integration batch.
