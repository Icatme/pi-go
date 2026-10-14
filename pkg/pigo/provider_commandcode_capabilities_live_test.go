package pigo

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// This paid-model probe requires an explicit model list in addition to the
// shared live-test gate. It never prints credentials or reasoning contents.
func TestCommandCodeCapabilitiesLive(t *testing.T) {
	if os.Getenv("PIGO_LIVE_TEST") != "1" || strings.TrimSpace(os.Getenv("PIGO_COMMANDCODE_LIVE_MODELS")) == "" {
		t.Skip("set PIGO_LIVE_TEST=1 and PIGO_COMMANDCODE_LIVE_MODELS=id,id to run")
	}
	apiKey := ResolveCommandCodeAPIKey(nil)
	if apiKey == "" {
		t.Fatal("Command Code credentials are not configured")
	}
	models, err := FetchCommandCodeModels(context.Background(), nil)
	if err != nil {
		t.Fatalf("fetch live model catalog: %v", err)
	}
	byID := make(map[string]Model, len(models))
	for _, model := range models {
		byID[model.ID] = model
	}
	for _, id := range strings.Split(os.Getenv("PIGO_COMMANDCODE_LIVE_MODELS"), ",") {
		id = strings.TrimSpace(id)
		t.Run(id, func(t *testing.T) {
			model, ok := byID[id]
			if !ok {
				t.Fatal("model missing from live catalog")
			}
			if !model.Reasoning || !modelSupportsInput(model, InputImage) {
				t.Fatal("select a model advertising both reasoning and image input")
			}
			reasoningLevel := ThinkingLevel(ClampThinkingLevel(model, ModelThinkingLevelHigh))
			if reasoningLevel == "off" {
				t.Fatal("select a model advertising a selectable reasoning effort")
			}
			t.Logf("model=%s advertised_api=%s reasoning=%s", id, commandCodeNativeModel(model).API, reasoningLevel)
			run := func(t *testing.T, ctx Context, level ThinkingLevel) (AssistantMessage, int) {
				t.Helper()
				started := time.Now()
				rawEvents := map[string]int{}
				rawParts := map[string]int{}
				rawThinkingChars, rawSignatureChars := 0, 0
				client := &http.Client{Transport: commandCodeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					response, err := http.DefaultTransport.RoundTrip(request)
					if response != nil {
						t.Logf("request_path=%s status=%d", request.URL.Path, response.StatusCode)
						if response.StatusCode >= 400 {
							body, readErr := io.ReadAll(io.LimitReader(response.Body, 8192))
							response.Body = &commandCodeReplayBody{Reader: io.MultiReader(bytes.NewReader(body), response.Body), Closer: response.Body}
							if readErr == nil {
								var envelope map[string]any
								if json.Unmarshal(body, &envelope) == nil {
									nested, _ := envelope["error"].(map[string]any)
									t.Logf("error_schema type=%v code=%v nested_type=%v nested_code=%v", envelope["type"], envelope["code"], nested["type"], nested["code"])
								}
							}
						}
					}
					return response, err
				})}
				stream := StreamSimple(model, ctx, SimpleStreamOptions{
					APIKey: apiKey, HTTPClient: client, Reasoning: level, MaxTokens: 4096, TimeoutMs: 90_000, MaxRetries: 0,
					OnPayload: func(payload any, _ Model) any {
						data, err := json.Marshal(payload)
						if err != nil {
							t.Error(err)
							return nil
						}
						var record map[string]any
						if err := json.Unmarshal(data, &record); err != nil {
							t.Error(err)
							return nil
						}
						if params, ok := record["params"].(map[string]any); ok {
							t.Logf("generate_payload model=%v effort=%v max_tokens=%v", params["model"], params["reasoning_effort"], params["max_tokens"])
							if level != "off" && params["reasoning_effort"] != string(level) {
								t.Error("selected reasoning effort missing from wire payload")
							}
						} else {
							t.Logf("native_payload model=%v thinking=%v reasoning=%v output_config=%v", record["model"], record["thinking"], record["reasoning"], record["output_config"])
						}
						prepared, err := commandCodeImageContext(model, ctx)
						if err != nil {
							t.Error(err)
							return nil
						}
						for _, msg := range prepared.Messages {
							if user, ok := msg.(UserMessage); ok {
								if blocks, ok := user.Content.([]ContentBlock); ok {
									for _, block := range blocks {
										if img, ok := block.(ImageContent); ok && !bytes.Contains(data, []byte(img.Data)) {
											t.Error("prepared image bytes changed or omitted in wire payload")
										}
									}
								}
							}
							if assistant, ok := msg.(AssistantMessage); ok && record["params"] == nil {
								for _, block := range assistant.Content {
									if thought, ok := block.(ThinkingContent); ok && commandCodeLiveOpaqueSignature(assistant.API, thought) != "" {
										signature, _ := json.Marshal(commandCodeLiveOpaqueSignature(assistant.API, thought))
										if !bytes.Contains(data, signature) {
											t.Error("signed thinking omitted from native replay")
										}
									}
								}
							}
						}
						return nil
					},
					OnResponse: func(response ProviderResponse, _ Model) {
						t.Logf("http_status=%d", response.Status)
					},
					OnProviderStreamEvent: func(data json.RawMessage, _ Model) error {
						var event map[string]any
						if err := json.Unmarshal(data, &event); err != nil {
							return err
						}
						rawEvents[anyString(event["type"])]++
						for _, field := range []string{"content_block", "delta", "item"} {
							if part, ok := event[field].(map[string]any); ok {
								rawParts[field+":"+anyString(part["type"])]++
								rawThinkingChars += len(anyString(part["thinking"]))
								rawSignatureChars += len(anyString(part["signature"])) + len(anyString(part["encrypted_content"]))
							}
						}
						return nil
					},
				})
				thinkingDeltas := 0
				for event := range stream.Events() {
					if event.Type == AssistantMessageEventThinkingDelta && strings.TrimSpace(event.Delta) != "" {
						thinkingDeltas++
					}
				}
				result := stream.Result()
				t.Logf("wire=%s response_id=%s stop=%s elapsed=%s thinking_deltas=%d usage=%+v event_types=%v", result.API, result.ResponseID, result.StopReason, time.Since(started).Round(time.Millisecond), thinkingDeltas, result.Usage, rawEvents)
				t.Logf("raw_parts=%v raw_thinking_chars=%d raw_signature_chars=%d response_model=%s", rawParts, rawThinkingChars, rawSignatureChars, result.ResponseModel)
				if result.StopReason == StopReasonError || result.StopReason == StopReasonAborted || result.StopReason == StopReasonLength {
					t.Fatalf("live request failed: stop=%s error=%s", result.StopReason, strings.ReplaceAll(result.ErrorMessage, apiKey, "[REDACTED]"))
				}
				return result, thinkingDeltas
			}
			t.Run("reasoning", func(t *testing.T) {
				ctx := Context{Messages: []Message{UserMessage{Content: "Find the smallest positive integer x such that x mod 7 = 3, x mod 11 = 5, and x mod 13 = 7. Return only the decimal integer."}}}
				result, deltas := run(t, ctx, reasoningLevel)
				if text := commandCodeLiveArithmeticAnswer(result); text != "423" {
					t.Errorf("incorrect reasoning answer: %q", text)
				}
				if deltas == 0 {
					// Both Messages and Responses may return only opaque reasoning.
					// Require an actual signature/encrypted payload, not just an ID.
					signedThinking := false
					for _, block := range result.Content {
						if thinking, ok := block.(ThinkingContent); ok && commandCodeLiveOpaqueSignature(result.API, thinking) != "" {
							signedThinking = true
						}
					}
					if signedThinking {
						t.Log("reasoning returned as an opaque signed block; no visible thinking text")
					} else {
						t.Error("no incremental or signed thinking received")
					}
				}
				ctx.Messages = append(ctx.Messages, result, UserMessage{Content: "Return only the decimal integer you gave in your previous answer."})
				replayed, _ := run(t, ctx, reasoningLevel)
				if text := commandCodeLiveArithmeticAnswer(replayed); text != "423" {
					t.Errorf("incorrect reasoning replay answer: %q", text)
				}
			})
			t.Run("tool_replay", func(t *testing.T) {
				ctx := Context{
					SystemPrompt: "Call lookup_validation_nonce exactly once with case_id cc-live-tool. After receiving its result, return only the nonce from that result.",
					Messages:     []Message{UserMessage{Content: "Retrieve the validation nonce using the tool."}},
					Tools: []Tool{{Name: "lookup_validation_nonce", Description: "Return the nonce for this validation case.", Parameters: map[string]any{
						"type": "object", "properties": map[string]any{"case_id": map[string]any{"type": "string"}}, "required": []string{"case_id"}, "additionalProperties": false,
					}}},
				}
				first, _ := run(t, ctx, reasoningLevel)
				var calls []ToolCall
				for _, block := range first.Content {
					if call, ok := block.(ToolCall); ok {
						calls = append(calls, call)
					}
				}
				if first.StopReason != StopReasonToolUse || len(calls) != 1 || calls[0].Name != "lookup_validation_nonce" || calls[0].Arguments["case_id"] != "cc-live-tool" {
					t.Fatalf("incorrect tool call: stop=%s calls=%+v", first.StopReason, calls)
				}
				var random [8]byte
				if _, err := rand.Read(random[:]); err != nil {
					t.Fatal(err)
				}
				nonce := hex.EncodeToString(random[:])
				ctx.Messages = append(ctx.Messages, first, ToolResultMessage{ToolCallID: calls[0].ID, ToolName: calls[0].Name, Content: []ContentBlock{TextContent{Text: `{"nonce":"` + nonce + `"}`}}})
				second, _ := run(t, ctx, reasoningLevel)
				if second.StopReason != StopReasonStop || strings.TrimSpace(commandCodeTextContent(second.Content)) != nonce {
					t.Fatalf("tool result not consumed: stop=%s text=%q", second.StopReason, commandCodeTextContent(second.Content))
				}
			})
			t.Run("image", func(t *testing.T) {
				format := strings.TrimSpace(os.Getenv("PIGO_COMMANDCODE_LIVE_IMAGE_FORMAT"))
				if format == "" {
					format = "png"
				}
				if format != "png" && format != "jpeg" {
					t.Fatal("PIGO_COMMANDCODE_LIVE_IMAGE_FORMAT must be png or jpeg")
				}
				// The expected colors exist only in pixels, never in the prompt.
				img := image.NewRGBA(image.Rect(0, 0, 384, 384))
				for i, rgb := range []color.RGBA{{255, 0, 0, 255}, {0, 0, 255, 255}, {0, 255, 0, 255}, {255, 255, 0, 255}} {
					x, y := (i%2)*192, (i/2)*192
					draw.Draw(img, image.Rect(x, y, x+192, y+192), &image.Uniform{C: rgb}, image.Point{}, draw.Src)
				}
				var encoded bytes.Buffer
				var err error
				if format == "jpeg" {
					err = jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 95})
				} else {
					err = png.Encode(&encoded, img)
				}
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("image_format=%s image_bytes=%d", format, encoded.Len())
				result, _ := run(t, Context{Messages: []Message{UserMessage{Content: []ContentBlock{
					TextContent{Text: "Name the four quadrant colors in row-major order: top-left, top-right, bottom-left, bottom-right. Return only lowercase English color names separated by commas, without spaces."},
					ImageContent{MIMEType: "image/" + format, Data: base64.StdEncoding.EncodeToString(encoded.Bytes())},
				}}}}, "off")
				if text := strings.TrimSpace(commandCodeTextContent(result.Content)); text != "red,blue,green,yellow" {
					t.Fatalf("incorrect image answer: %q", text)
				}
			})
		})
	}
}

func commandCodeLiveOpaqueSignature(api API, thinking ThinkingContent) string {
	if api == "anthropic-messages" {
		return thinking.ThinkingSignature
	}
	if api == "openai-responses" {
		var item struct {
			Type             string `json:"type"`
			EncryptedContent string `json:"encrypted_content"`
		}
		if json.Unmarshal([]byte(thinking.ThinkingSignature), &item) == nil && item.Type == "reasoning" {
			return item.EncryptedContent
		}
	}
	return ""
}

func commandCodeLiveArithmeticAnswer(message AssistantMessage) string {
	text := strings.TrimSpace(commandCodeTextContent(message.Content))
	// Check the arithmetic, allowing one Markdown wrapper around the integer.
	// Extra prose, additional values and wrong answers still fail.
	for _, wrapper := range []string{"**", "`"} {
		if len(text) >= 2*len(wrapper) && strings.HasPrefix(text, wrapper) && strings.HasSuffix(text, wrapper) {
			return text[len(wrapper) : len(text)-len(wrapper)]
		}
	}
	return text
}
