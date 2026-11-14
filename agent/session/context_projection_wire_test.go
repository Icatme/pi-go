package session

import (
	"encoding/json"
	"fmt"
	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/pkg/pigo"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestContextProjectionAnthropicWire(t *testing.T) {
	models := pigo.GetModels("anthropic")
	if len(models) == 0 {
		t.Fatal("missing builtin models")
	}
	model := models[0]
	s := projectionSession(t)
	old := projectionAssistant()
	old.Model = model.ID
	old.Parts[0].Signature = "OLD_SIGNED_SECRET"
	old.Parts[1].Signature = "OLD_REDACTED_SECRET"
	old.StopReason = agent.StopReasonToolUse
	calls := []agent.ToolCall{{ID: "call_a", Name: "lookup", Arguments: json.RawMessage(`{"n":1}`)}, {ID: "call_b", Name: "lookup", Arguments: json.RawMessage(`{"n":2}`)}}
	old.ToolCalls = calls
	tail := []agent.Message{agent.NewUserTextMessage("retained"), old}
	for _, c := range calls {
		tail = append(tail, agent.NewToolResultMessage(c, agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "result-" + c.ID}}}, false))
	}
	if _, err := s.AppendCompaction(MainLane, CompactionData{Summary: "summary", RetainedTail: tail}); err != nil {
		t.Fatal(err)
	}
	later := projectionAssistant()
	later.Model = model.ID
	later.StopReason = agent.StopReasonStop
	later.Parts = later.Parts[:1]
	later.Parts[0].Signature = "NEW_SIGNED_SECRET"
	if _, err := s.AppendMessages(MainLane, []agent.Message{agent.NewUserTextMessage("later"), later}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.State()
	beforeJSON, _ := json.Marshal(before)
	opts := projectionTarget()
	opts.TargetModel = model.ID
	view, err := s.ProjectContext(MainLane, opts)
	if err != nil {
		t.Fatal(err)
	}
	var body []byte
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var raw json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Error(err)
		}
		body = append([]byte(nil), raw...)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"fixture\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"fixture\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	snapshot := &agent.AgentSnapshot{Messages: append([]agent.Message{agent.NewUserTextMessage(view.Summary)}, view.Messages...), Model: agent.ModelRef{Provider: "anthropic", Model: model.ID, ProviderConfig: agent.ProviderConfig{BaseURL: server.URL, APIKey: "fixture-only"}}}
	out, err := agent.NewEngine().Run(t.Context(), agent.AgentDefinition{}, snapshot, []agent.Message{agent.NewUserTextMessage("next")}, nil)
	if err != nil || out.Error != "" {
		t.Fatalf("run err=%v snapshot=%+v", err, out)
	}
	if requests != 1 || strings.Contains(string(body), "OLD_SIGNED_SECRET") || strings.Contains(string(body), "OLD_REDACTED_SECRET") || !strings.Contains(string(body), "NEW_SIGNED_SECRET") {
		t.Fatalf("unsafe wire count=%d body=%s", requests, body)
	}
	var wire struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err = json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range wire.Messages {
		if len(m.Content) == 0 || m.Content[0] != '[' {
			continue
		}
		var blocks []struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			ToolUseID string `json:"tool_use_id"`
		}
		if err = json.Unmarshal(m.Content, &blocks); err != nil {
			t.Fatal(err)
		}
		for _, b := range blocks {
			if b.Type == "tool_use" {
				got = append(got, "call:"+b.ID)
			}
			if b.Type == "tool_result" {
				got = append(got, "result:"+b.ToolUseID)
			}
		}
	}
	if strings.Join(got, ",") != "call:call_a,call:call_b,result:call_a,result:call_b" {
		t.Fatal(got)
	}
	after, _ := s.State()
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatal("source audit changed")
	}
}
func TestContextProjectionThinkingImageWireBoundary(t *testing.T) {
	s := projectionSession(t)
	m := projectionAssistant()
	m.Parts = append(m.Parts[:2], agent.Part{Type: agent.PartTypeImage, Data: "eA==", MIMEType: "image/png"})
	if _, err := s.AppendCompaction(MainLane, CompactionData{Summary: "summary", RetainedTail: []agent.Message{agent.NewUserTextMessage("q"), m}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProjectContext(MainLane, projectionTarget()); err == nil {
		t.Fatal("image-only assistant projected despite unsupported Anthropic assistant image conversion")
	}
}
