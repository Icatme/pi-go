// Package pigo exposes the core pi.ai-style library surface for Go.
//
// The package is intentionally a small core library, not a full agent framework.
// It focuses on model metadata, provider and API routing, streaming assistant
// events, message transformation for replay across providers, and minimal tool
// argument validation.
//
// Core concepts:
//
//   - Model describes a concrete model plus provider-specific compatibility
//     metadata and capabilities.
//   - Provider and API registries route Stream and Complete calls without a
//     hard-coded provider switch.
//   - Context carries the system prompt, message history, and tools.
//   - Message and ContentBlock capture the normalized conversation format used
//     across providers.
//   - AssistantMessageEventStream exposes incremental streaming events and a
//     final AssistantMessage result.
//
// Basic usage:
//
//	model := pigo.GetModel("kimi-coding", "kimi-k2-thinking")
//	if model == nil {
//		panic("missing model")
//	}
//
//	result := pigo.CompleteSimple(*model, pigo.Context{
//		SystemPrompt: "You are a precise coding assistant.",
//		Messages: []pigo.Message{
//			pigo.UserMessage{Content: "Summarize this repository."},
//		},
//	}, pigo.SimpleStreamOptions{
//		APIKey: "kimi-api-key",
//	})
//
//	_ = result
//
// StreamSimple provides the same execution path with incremental events when a
// caller needs text deltas, tool-call lifecycle events, or explicit stop/error
// handling.
package pigo
