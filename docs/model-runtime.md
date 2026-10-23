# Model calls and streaming

[Documentation index](README.md) · [Quick start](../README.md#quick-start) · [Provider guide](providers.md)

`pkg/pigo` owns model metadata, provider protocols, authentication, and model
streaming. A `Context` carries the system prompt, messages, and tool declarations;
an `AssistantMessage` contains text, reasoning, tool calls, usage, and a stop
reason. Applications that need a tool-execution loop can use [agent](../agent/README.md).

## Completion APIs

| API | Result | Options |
| --- | --- | --- |
| `CompleteSimple` | Final `AssistantMessage` | `SimpleStreamOptions` |
| `StreamSimple` | Incremental events and a final message | `SimpleStreamOptions` |
| `Complete` | Final `AssistantMessage` | `ProviderStreamOptions` |
| `Stream` | Incremental events and a final message | `ProviderStreamOptions` |

The [root quick start](../README.md#quick-start) is a complete blocking-call
example. Use `RequestContext` to supply cancellation or a deadline. Check the
result's `StopReason`: errors and cancellation are represented by
`StopReasonError` and `StopReasonAborted`, not a separate Go error return.

## Streaming

This helper prints text deltas and checks the final result. The caller supplies
the context, model, and API key:

```go
import (
    "context"
    "fmt"

    "github.com/Icatme/pi-go/pkg/pigo"
)

func streamText(ctx context.Context, model pigo.Model, apiKey string) error {
    stream := pigo.StreamSimple(model, pigo.Context{
        Messages: []pigo.Message{pigo.UserMessage{Content: "Hello"}},
    }, pigo.SimpleStreamOptions{
        APIKey:         apiKey,
        RequestContext: ctx,
    })
    defer stream.Close()

    for event := range stream.Events() {
        if event.Type == pigo.AssistantMessageEventTextDelta {
            fmt.Print(event.Delta)
        }
    }
    result := stream.Result()
    if result.StopReason == pigo.StopReasonError || result.StopReason == pigo.StopReasonAborted {
        return fmt.Errorf("model request %s: %s", result.StopReason, result.ErrorMessage)
    }
    return nil
}
```

Drain `Events()` before waiting for `Result()`. If you stop reading early, close
the stream or cancel its request context. Consume the final result once.
The stream also exposes thinking and tool-call lifecycle events. Delta delivery
is bounded and may drop deltas under backpressure; use the final message as the
complete response rather than reconstructing it solely from displayed deltas.

## Sampling options

OpenAI Completions and Responses accept model and per-request sampling fields.
Given a model using one of those APIs:

```go
model.SamplingParams = pigo.SamplingParams{"top_p": 0.95}
model.SamplingParamsByThinkingLevel = pigo.SamplingParamsByThinkingLevel{
    pigo.ModelThinkingLevelHigh: {"temperature": 0.8},
}
options := pigo.ProviderStreamOptions{
    Reasoning:      pigo.ThinkingLevelHigh,
    SamplingParams: pigo.SamplingParams{"top_p": 0.5},
}
_ = options // Pass to Complete or Stream with the selected model and context.
```

Sampling fields merge per key: model defaults, then effective thinking-level
overrides, then request `SamplingParams`. The merged fields override named
`Temperature`/`TopP` fields, including explicit zero or null values. Responses
validates the final temperature and top_p against its registered model capabilities.
Protocol fields such as `model`, `reasoning`, `tools`, and `stream` are rejected
in sampling maps; use the dedicated request options for those controls.
Other APIs, including Codex, ignore these sampling maps. `Stream` and
`StreamSimple` use the same precedence. Responses keeps the logical pi thinking
level during option normalization and translates it at request serialization;
a summary-only request uses `medium`.

Codex identity-header behavior is documented in the
[provider guide](providers.md#openai-codex-identity-headers).

## Failure diagnostics

`ProviderFailureOf` reads the final classified provider failure from an assistant
message's diagnostics, without interpreting its error text. It distinguishes
upstream errors, interrupted streams, response parsing, and SDK failures.
Diagnostics survive message cloning and JSON storage. They are evidence for the
host's error policy, not permission to retry; provider error text can contain
private data and should not be exposed through a public API or unrestricted log.

## Runtime boundaries

- The provider registry lazily loads provider modules and model metadata.
- The API registry dispatches streams by `model.API`.
- Shared message/content types support replay and serialization across providers.
- HTTP/SSE helpers and optional Codex WebSocket transport belong to the provider layer.
- Observer hooks expose request completion/errors and stream-finish accounting.
- Built-in tool argument validation implements a limited schema subset; the host
  remains responsible for business validation and execution permissions.
