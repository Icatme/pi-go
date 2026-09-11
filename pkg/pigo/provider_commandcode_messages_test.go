package pigo

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCommandCodeHistoryOmitsReasoningAndPreservesInterruptedTools(t *testing.T) {
	model := commandCodeTestModel("")
	messages := []Message{
		AssistantMessage{Content: []ContentBlock{ThinkingContent{Thinking: "prior private reasoning"}}},
		AssistantMessage{Content: []ContentBlock{TextContent{Text: "working"}, ThinkingContent{Thinking: "do not replay"}, ToolCall{ID: "interrupted", Name: "read"}, ToolCall{ID: "completed", Name: "read", Arguments: map[string]any{"path": "a.go"}}}},
		ToolResultMessage{ToolCallID: "completed", ToolName: "read", Content: []ContentBlock{TextContent{Text: "ok"}}},
		ToolResultMessage{ToolCallID: "orphan", Content: []ContentBlock{TextContent{Text: "orphan output"}}},
	}
	got, err := commandCodeMessages(model, messages)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(got)
	if len(got) != 3 || strings.Contains(string(body), "reasoning") || strings.Contains(string(body), "orphan output") {
		t.Fatalf("invalid history: %s", body)
	}
	assistant := commandCodeAnySlice(commandCodeRecord(got[0])["content"])
	if len(assistant) != 3 {
		t.Fatalf("lost visible content/calls: %s", body)
	}
	missing := commandCodeAnySlice(commandCodeRecord(got[1])["content"])
	result := commandCodeRecord(missing[0])
	if result["toolCallId"] != "interrupted" || commandCodeRecord(result["output"])["type"] != "error-text" {
		t.Fatalf("missing interrupted result: %s", body)
	}
	if _, ok := messages[0].(AssistantMessage).Content[0].(ThinkingContent); !ok {
		t.Fatal("mutated caller history")
	}
}

func TestCommandCodeImageSerializationAndModelSwitch(t *testing.T) {
	image := ImageContent{Data: "aGVsbG8=", MIMEType: "image/png"}
	vision := newCommandCodeModel("gpt-5.6-luna", "Luna", 1050000, 65536, UsageCost{})
	direct := []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "describe"}, image}}}
	got, err := commandCodeMessages(vision, direct)
	if err != nil {
		t.Fatal(err)
	}
	parts := commandCodeAnySlice(commandCodeRecord(got[0])["content"])
	if commandCodeRecord(parts[1])["image"] != "data:image/png;base64,aGVsbG8=" || commandCodeRecord(parts[1])["data"] != nil {
		t.Fatalf("wrong image wire: %+v", parts[1])
	}
	text := commandCodeTestModel("")
	if _, err := commandCodeMessages(text, direct); err == nil {
		t.Fatal("direct image silently accepted by text model")
	}
	history := []Message{AssistantMessage{Content: []ContentBlock{ToolCall{ID: "screenshot", Name: "capture"}}}, ToolResultMessage{ToolCallID: "screenshot", ToolName: "capture", Content: []ContentBlock{TextContent{Text: "captured page"}, image}}}
	got, err = commandCodeMessages(vision, history)
	if err != nil || len(got) != 3 || commandCodeRecord(got[2])["role"] != "user" {
		t.Fatalf("tool image lost: %+v %v", got, err)
	}
	got, err = commandCodeMessages(text, history)
	if err != nil || len(got) != 2 {
		t.Fatalf("model switch failed: %+v %v", got, err)
	}
	body, _ := json.Marshal(got)
	if !strings.Contains(string(body), "captured page") || strings.Contains(string(body), "data:image") {
		t.Fatalf("model switch changed tool text: %s", body)
	}
	malformed := []Message{UserMessage{Content: []ContentBlock{ImageContent{Data: "data", MIMEType: ""}}}}
	if _, err = commandCodeMessages(vision, malformed); err == nil {
		t.Fatal("invalid image accepted")
	}
}

func TestCommandCodeCatalogCapabilitiesAndPriceSnapshot(t *testing.T) {
	luna := newCommandCodeModel("gpt-5.6-luna", "Luna", 1050000, 1, commandCodeModelCosts["gpt-5.6-luna"])
	if !luna.Reasoning || len(luna.Input) != 2 || luna.ThinkingLevelMap[ModelThinkingLevelMax] != "max" || luna.Cost.Input != 0.2 || luna.Cost.Output != 1.2 {
		t.Fatalf("Luna metadata stale: %+v", luna)
	}
	muse := newCommandCodeModel("meta/muse-spark-1.3-contributor", "Muse", 1048576, 1, UsageCost{})
	if !muse.Reasoning || muse.ThinkingLevelMap[ModelThinkingLevelXHigh] != "xhigh" || muse.ThinkingLevelMap[ModelThinkingLevelMax] != "" {
		t.Fatalf("Muse override missing: %+v", muse)
	}
	glm := newCommandCodeModel("z-ai/glm-5.3-flash", "GLM", 200000, 65536, UsageCost{})
	if glm.MaxTokens != 131072 || glm.ThinkingLevelMap[ModelThinkingLevelLow] != "low" {
		t.Fatalf("GLM catalog stale: %+v", glm)
	}
	unknown := newCommandCodeModel("unknown/model", "Unknown", 32000, 32000, UsageCost{})
	if unknown.Reasoning || len(unknown.Input) != 1 || unknown.Capabilities.Reasoning != CapabilityUnknown {
		t.Fatalf("unknown claims unsupported facts: %+v", unknown)
	}
	luna.Input[0] = InputImage
	luna.ThinkingLevelMap[ModelThinkingLevelHigh] = "changed"
	fresh := newCommandCodeModel("gpt-5.6-luna", "Luna", 1050000, 65536, UsageCost{})
	if fresh.Input[0] != InputText || fresh.ThinkingLevelMap[ModelThinkingLevelHigh] != "high" {
		t.Fatal("model mutations changed catalog")
	}
}

func TestCommandCodeOfficialEnvironmentAndPlaceholderAliases(t *testing.T) {
	t.Setenv("COMMAND_CODE_API_KEY", "official-key")
	t.Setenv("COMMANDCODE_API_KEY", "legacy-key")
	if got := ResolveCommandCodeAPIKey(nil); got != "official-key" {
		t.Fatalf("official env precedence: %q", got)
	}
	for _, placeholder := range []string{"COMMAND_CODE_API_KEY", "$COMMAND_CODE_API_KEY", "COMMANDCODE_API_KEY", "$COMMANDCODE_API_KEY"} {
		if got := usableCommandCodeAPIKey(placeholder); got != "" {
			t.Fatalf("placeholder leaked: %q", got)
		}
	}
	t.Setenv("CMD_ZDR", "1")
	if !commandCodeZDR() {
		t.Fatal("official ZDR ignored")
	}
}
