package main

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	core "github.com/Icatme/pi-go/agent"
)

func TestChatPresetPromptUpdatesExistingSession(t *testing.T) {
	var prompts []string
	model := fakeStreamModel(func(request core.ModelRequest) string {
		prompts = append(prompts, request.SystemPrompt)
		return "answer"
	})
	config := AppConfig{DataDir: t.TempDir(), Provider: "openai-codex", Model: "gpt-5.5", ChatModel: model, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	app, err := NewApp(config)
	if err != nil {
		t.Fatal(err)
	}
	preset := PresetSpec{Name: "chat", Mode: RuntimeModeChat, SystemPrompt: "old policy"}
	if err := app.sendChat(context.Background(), preset, "first"); err != nil {
		t.Fatal(err)
	}
	preset.SystemPrompt = "updated policy"
	if err := app.sendChat(context.Background(), preset, "second"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prompts, []string{"old policy", "updated policy"}) {
		t.Fatalf("model saw stale preset prompt: %q", prompts)
	}
	before := app.registry.Sessions["chat"].ChatSnapshot
	if len(before.Messages) != 6 || before.Messages[3].Role != core.RoleSystem || before.Messages[1].Parts[0].Text != "first" {
		t.Fatalf("preset update did not preserve history with one section patch: %+v", before.Messages)
	}
	reloaded, err := NewApp(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := reloaded.sendChat(context.Background(), preset, "third"); err != nil {
		t.Fatal(err)
	}
	after := reloaded.registry.Sessions["chat"].ChatSnapshot
	if len(after.Messages) != 8 || prompts[2] != "updated policy" {
		t.Fatalf("unchanged preset created another patch or reverted after reload: messages=%+v prompts=%q", after.Messages, prompts)
	}
}

func TestChatPresetPromptMigratesLeadingContentAndKeepsSystemChanges(t *testing.T) {
	otherSection := "other section"
	head := core.NewSystemMessage(core.SystemMessagePayload{
		Content: "old policy", Sections: map[string]*string{"other": &otherSection},
		ToolsAdded: []core.ToolDeclaration{{Name: "lookup", Parameters: map[string]any{"type": "object"}}},
	})
	source := &core.AgentSnapshot{SystemPrompt: "old policy", Messages: []core.Message{
		head, core.NewUserTextMessage("first"), core.NewTextMessage(core.RoleAssistant, "answer"),
		core.NewSystemMessage(core.SystemMessagePayload{Content: "extra instruction"}),
	}}
	snapshot := cloneSnapshotPtr(source)
	setChatPresetPrompt(snapshot, "updated policy")
	if source.Messages[0].System.Content != "old policy" || len(source.Messages[0].System.Sections) != 1 {
		t.Fatal("preset migration mutated the stored source snapshot")
	}
	if len(snapshot.Messages) != 5 || snapshot.Messages[0].System.Content != "" || *snapshot.Messages[0].System.Sections[chatPresetSection] != "old policy" {
		t.Fatalf("initial preset content was not migrated into its section: %+v", snapshot.Messages)
	}
	if got := core.GetCurrentSystemPrompt(snapshot.Messages); got != "extra instruction\n\nother section\n\nupdated policy" {
		t.Fatalf("preset migration changed other instructions: %q", got)
	}
	if tools := core.GetCurrentTools(snapshot.Messages); len(tools) != 1 || tools[0].Name != "lookup" {
		t.Fatalf("preset migration lost tool declarations: %+v", tools)
	}
	snapshot.Messages[0].System.ToolsAdded[0].Parameters["type"] = "changed"
	if source.Messages[0].System.ToolsAdded[0].Parameters["type"] != "object" {
		t.Fatal("migrated tool schema aliases the source snapshot")
	}
	setChatPresetPrompt(snapshot, "updated policy")
	if len(snapshot.Messages) != 5 {
		t.Fatal("unchanged migrated preset added another system message")
	}
}

func TestChatSessionsPersistAcrossSwitchAndRestart(t *testing.T) {
	dataDir := t.TempDir()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	app, err := NewApp(AppConfig{
		DataDir:         dataDir,
		Provider:        "openai-codex",
		Model:           "gpt-5.5",
		ChatModel:       fakeChatModel("chat"),
		ReflectionModel: fakeReflectionModel(),
		Stdout:          stdout,
		Stderr:          stderr,
	})
	if err != nil {
		t.Fatalf("NewApp returned error: %v", err)
	}

	if _, err := app.HandleLine(context.Background(), "hello"); err != nil {
		t.Fatalf("first chat message returned error: %v", err)
	}
	if _, err := app.HandleLine(context.Background(), "/use coder"); err != nil {
		t.Fatalf("switch to coder returned error: %v", err)
	}
	if _, err := app.HandleLine(context.Background(), "ship it"); err != nil {
		t.Fatalf("coder message returned error: %v", err)
	}
	if _, err := app.HandleLine(context.Background(), "/use chat"); err != nil {
		t.Fatalf("switch back to chat returned error: %v", err)
	}

	chatSession := app.registry.Sessions["chat"]
	coderSession := app.registry.Sessions["coder"]
	if chatSession.ChatSnapshot == nil || len(chatSession.ChatSnapshot.Messages) != 3 {
		t.Fatalf("expected chat snapshot to contain one turn, got %+v", chatSession.ChatSnapshot)
	}
	if coderSession.ChatSnapshot == nil || len(coderSession.ChatSnapshot.Messages) != 3 {
		t.Fatalf("expected coder snapshot to contain one turn, got %+v", coderSession.ChatSnapshot)
	}
	for _, snapshot := range []*core.AgentSnapshot{chatSession.ChatSnapshot, coderSession.ChatSnapshot} {
		if snapshot.Messages[0].Role != core.RoleSystem || snapshot.Messages[1].Role != core.RoleUser || snapshot.Messages[2].Role != core.RoleAssistant || core.GetCurrentSystemPrompt(snapshot.Messages) != snapshot.SystemPrompt {
			t.Fatalf("expected one system head and a complete turn: %+v", snapshot.Messages)
		}
	}

	reloaded, err := NewApp(AppConfig{
		DataDir:         dataDir,
		Provider:        "openai-codex",
		Model:           "gpt-5.5",
		ChatModel:       fakeChatModel("chat"),
		ReflectionModel: fakeReflectionModel(),
		Stdout:          &bytes.Buffer{},
		Stderr:          &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("reloaded NewApp returned error: %v", err)
	}
	if _, err := reloaded.HandleLine(context.Background(), "again"); err != nil {
		t.Fatalf("reloaded chat message returned error: %v", err)
	}

	reloadedChat := reloaded.registry.Sessions["chat"]
	if reloadedChat.ChatSnapshot == nil {
		t.Fatal("expected reloaded chat snapshot")
	}
	if got := len(reloadedChat.ChatSnapshot.Messages); got != 5 {
		t.Fatalf("expected reloaded chat session to continue from previous transcript, got %d messages", got)
	}
	for _, message := range reloadedChat.ChatSnapshot.Messages[1:] {
		if message.Role == core.RoleSystem {
			t.Fatal("restart duplicated the system head")
		}
	}
}

func TestReflectionTranscriptIsPersistedWithoutLiveSnapshot(t *testing.T) {
	dataDir := t.TempDir()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	var criticPrompts []string
	critic := fakeStreamModel(func(request core.ModelRequest) string {
		criticPrompts = append(criticPrompts, request.SystemPrompt)
		return `{"verdict":"accept","summary":"The draft satisfies the request.","revision_instructions":[]}`
	})

	app, err := NewApp(AppConfig{
		DataDir:          dataDir,
		Provider:         "openai-codex",
		Model:            "gpt-5.5",
		ChatModel:        fakeChatModel("chat"),
		ReflectionModel:  fakeReflectionModel(),
		ReflectionCritic: critic,
		Preset:           "reflect",
		Stdout:           stdout,
		Stderr:           stderr,
	})
	if err != nil {
		t.Fatalf("NewApp returned error: %v", err)
	}

	if _, err := app.HandleLine(context.Background(), "alpha"); err != nil {
		t.Fatalf("first reflection message returned error: %v", err)
	}
	if _, err := app.HandleLine(context.Background(), "beta"); err != nil {
		t.Fatalf("second reflection message returned error: %v", err)
	}

	session := app.registry.Sessions["reflect"]
	if session.ChatSnapshot != nil {
		t.Fatalf("expected reflection mode to avoid snapshot persistence, got %+v", session.ChatSnapshot)
	}
	if got := len(session.Transcript); got != 4 {
		t.Fatalf("expected reflection transcript to store two visible turns, got %d messages", got)
	}
	if text := messageText(session.Transcript[3]); !strings.Contains(text, "draft:beta") {
		t.Fatalf("expected final reflection transcript entry to be the latest draft, got %q", text)
	}
	if len(criticPrompts) != 2 {
		t.Fatalf("expected one structured critic call per draft, got %d", len(criticPrompts))
	}
	wantCriticPrompt := app.presetIndex["reflect"].ReflectionPrompt
	for _, prompt := range criticPrompts {
		if prompt != wantCriticPrompt {
			t.Fatalf("critic system prompt=%q, want preset prompt %q", prompt, wantCriticPrompt)
		}
	}
}

func TestResetCreatesFreshChatSession(t *testing.T) {
	dataDir := t.TempDir()
	app, err := NewApp(AppConfig{
		DataDir:         dataDir,
		Provider:        "openai-codex",
		Model:           "gpt-5.5",
		ChatModel:       fakeChatModel("chat"),
		ReflectionModel: fakeReflectionModel(),
		Stdout:          &bytes.Buffer{},
		Stderr:          &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("NewApp returned error: %v", err)
	}

	if _, err := app.HandleLine(context.Background(), "hello"); err != nil {
		t.Fatalf("chat message returned error: %v", err)
	}

	before := app.registry.Sessions["chat"].ChatSnapshot.SessionID
	if _, err := app.HandleLine(context.Background(), "/reset"); err != nil {
		t.Fatalf("reset returned error: %v", err)
	}

	after := app.registry.Sessions["chat"].ChatSnapshot.SessionID
	if before == after {
		t.Fatalf("expected reset to create a fresh session id, kept %q", after)
	}
	if got := len(app.registry.Sessions["chat"].Transcript); got != 0 {
		t.Fatalf("expected reset transcript to be empty, got %d messages", got)
	}
}

func fakeChatModel(prefix string) core.StreamModel {
	return fakeStreamModel(func(request core.ModelRequest) string {
		return prefix + ":" + messageText(request.Messages[len(request.Messages)-1])
	})
}

func fakeReflectionModel() core.StreamModel {
	return fakeStreamModel(func(request core.ModelRequest) string {
		text := messageText(request.Messages[len(request.Messages)-1])
		return "draft:" + text
	})
}

func fakeStreamModel(fn func(core.ModelRequest) string) core.StreamModel {
	return core.StreamFunc(func(_ context.Context, request core.ModelRequest) (core.AssistantStream, error) {
		text := fn(request)
		message := core.Message{
			Role:       core.RoleAssistant,
			StopReason: core.StopReasonStop,
			Parts: []core.Part{
				{Type: core.PartTypeText, Text: text},
			},
		}
		stream := &fakeAssistantStream{
			events: make(chan core.AssistantEvent, 2),
			final:  message,
		}
		stream.events <- core.AssistantEvent{Type: core.AssistantEventStart, Message: message}
		stream.events <- core.AssistantEvent{Type: core.AssistantEventTextDelta, Message: message, Delta: text}
		close(stream.events)
		return stream, nil
	})
}

type fakeAssistantStream struct {
	events chan core.AssistantEvent
	final  core.Message
}

func (s *fakeAssistantStream) Events() <-chan core.AssistantEvent {
	return s.events
}

func (s *fakeAssistantStream) Wait() (core.Message, error) {
	return s.final, nil
}

func (s *fakeAssistantStream) Close() error {
	return nil
}
