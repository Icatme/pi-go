package session

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func TestPrepareCompactionCutsOnlyAtCompleteTurnBoundary(t *testing.T) {
	t.Parallel()

	branch := compactionMessageBranch(t,
		agent.NewTextMessage(agent.RoleUser, "u1"),
		agent.NewTextMessage(agent.RoleAssistant, "a1"),
		agent.NewTextMessage(agent.RoleUser, "u2"),
		agent.NewTextMessage(agent.RoleAssistant, "a2"),
		agent.NewTextMessage(agent.RoleUser, "u3"),
		agent.NewTextMessage(agent.RoleAssistant, "a3"),
	)
	plan, err := PrepareCompaction(branch, CompactionOptions{
		KeepRecentTokens: 3,
		EstimateTokens:   func(agent.Message) int64 { return 1 },
	})
	if err != nil {
		t.Fatalf("PrepareCompaction() error = %v", err)
	}
	if plan == nil {
		t.Fatal("PrepareCompaction() plan = nil")
	}
	assertMessageTexts(t, plan.MessagesToSummarize, "u1", "a1")
	assertMessageTexts(t, plan.RetainedTail, "u2", "a2", "u3", "a3")
	if plan.TokensBefore != 6 {
		t.Fatalf("TokensBefore = %d, want 6", plan.TokensBefore)
	}
}

func TestPrepareCompactionKeepsAssistantToolCallAndResultsTogether(t *testing.T) {
	t.Parallel()

	toolAssistant := agent.NewTextMessage(agent.RoleAssistant, "calling")
	toolAssistant.ToolCalls = []agent.ToolCall{
		{ID: "call-1", OriginalID: "raw-1", Name: "one", Arguments: json.RawMessage(`{"value":1}`)},
		{ID: "call-2", Name: "two", Arguments: json.RawMessage(`{"value":2}`)},
	}
	result1 := agent.NewToolResultMessage(toolAssistant.ToolCalls[0], agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "one"}}}, false)
	result2 := agent.NewToolResultMessage(toolAssistant.ToolCalls[1], agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "two"}}}, false)
	branch := compactionMessageBranch(t,
		agent.NewTextMessage(agent.RoleUser, "old user"),
		agent.NewTextMessage(agent.RoleAssistant, "old answer"),
		agent.NewTextMessage(agent.RoleUser, "tool user"),
		toolAssistant,
		result1,
		result2,
		agent.NewTextMessage(agent.RoleAssistant, "tool final"),
	)

	plan, err := PrepareCompaction(branch, CompactionOptions{
		KeepRecentTokens: 0,
		EstimateTokens:   func(agent.Message) int64 { return 100 },
	})
	if err != nil {
		t.Fatalf("PrepareCompaction() error = %v", err)
	}
	if plan == nil {
		t.Fatal("PrepareCompaction() plan = nil")
	}
	assertMessageTexts(t, plan.MessagesToSummarize, "old user", "old answer")
	if len(plan.RetainedTail) != 5 {
		t.Fatalf("retained tail count = %d, want 5", len(plan.RetainedTail))
	}
	if len(plan.RetainedTail[1].ToolCalls) != 2 {
		t.Fatalf("retained assistant tool calls = %#v", plan.RetainedTail[1].ToolCalls)
	}
	if plan.RetainedTail[2].ToolResult == nil || plan.RetainedTail[3].ToolResult == nil {
		t.Fatalf("retained tail split tool results: %#v", plan.RetainedTail)
	}
}

func TestPrepareCompactionRejectsIncompleteLatestToolGroup(t *testing.T) {
	t.Parallel()

	openAssistant := agent.NewTextMessage(agent.RoleAssistant, "calling")
	openAssistant.ToolCalls = []agent.ToolCall{{ID: "open-call", Name: "lookup"}}
	branch := compactionMessageBranch(t,
		agent.NewTextMessage(agent.RoleUser, "old user"),
		agent.NewTextMessage(agent.RoleAssistant, "old answer"),
		agent.NewTextMessage(agent.RoleUser, "new user"),
		openAssistant,
	)

	_, err := PrepareCompaction(branch, CompactionOptions{KeepRecentTokens: 0})
	if !errors.Is(err, &Error{Code: ErrorInvalidEntry}) {
		t.Fatalf("PrepareCompaction() error = %v, want invalid_entry", err)
	}
}

func TestPrepareCompactionRejectsBrokenToolPairing(t *testing.T) {
	t.Parallel()

	toolAssistant := agent.NewTextMessage(agent.RoleAssistant, "calling")
	toolAssistant.ToolCalls = []agent.ToolCall{{ID: "call-1", Name: "lookup"}}
	tests := []struct {
		name     string
		messages []agent.Message
	}{
		{
			name: "new user crosses pending call",
			messages: []agent.Message{
				agent.NewTextMessage(agent.RoleUser, "u1"),
				toolAssistant,
				agent.NewTextMessage(agent.RoleUser, "u2"),
			},
		},
		{
			name: "unmatched result",
			messages: []agent.Message{
				agent.NewTextMessage(agent.RoleUser, "u1"),
				agent.NewToolResultMessage(agent.ToolCall{ID: "missing", Name: "lookup"}, agent.ToolResult{}, false),
				agent.NewTextMessage(agent.RoleUser, "u2"),
			},
		},
		{
			name: "assistant crosses pending call",
			messages: []agent.Message{
				agent.NewTextMessage(agent.RoleUser, "u1"),
				toolAssistant,
				agent.NewTextMessage(agent.RoleAssistant, "unexpected"),
				agent.NewTextMessage(agent.RoleUser, "u2"),
			},
		},
		{
			name: "duplicate pending call id",
			messages: func() []agent.Message {
				duplicate := agent.NewTextMessage(agent.RoleAssistant, "duplicate")
				duplicate.ToolCalls = []agent.ToolCall{
					{ID: "same", Name: "one"},
					{ID: "same", Name: "two"},
				}
				return []agent.Message{
					agent.NewTextMessage(agent.RoleUser, "u1"),
					duplicate,
				}
			}(),
		},
		{
			name: "duplicate result",
			messages: func() []agent.Message {
				call := agent.ToolCall{ID: "call-1", Name: "lookup"}
				assistant := agent.NewTextMessage(agent.RoleAssistant, "calling")
				assistant.ToolCalls = []agent.ToolCall{call}
				result := agent.NewToolResultMessage(call, agent.ToolResult{}, false)
				return []agent.Message{
					agent.NewTextMessage(agent.RoleUser, "u1"),
					assistant,
					result,
					result,
				}
			}(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			branch := compactionMessageBranch(t, test.messages...)
			_, err := PrepareCompaction(branch, CompactionOptions{KeepRecentTokens: 0})
			if !errors.Is(err, &Error{Code: ErrorInvalidEntry}) {
				t.Fatalf("PrepareCompaction() error = %v, want invalid_entry", err)
			}
		})
	}
}

func TestPrepareCompactionMatchesToolResultByOriginalID(t *testing.T) {
	t.Parallel()

	assistant := agent.NewTextMessage(agent.RoleAssistant, "calling")
	assistant.ToolCalls = []agent.ToolCall{{ID: "normalized", OriginalID: "raw", Name: "lookup"}}
	result := agent.NewTextMessage(agent.RoleTool, "done")
	result.ToolResult = &agent.ToolResultPayload{OriginalToolCallID: "raw", ToolName: "lookup"}
	branch := compactionMessageBranch(t,
		agent.NewTextMessage(agent.RoleUser, "old user"),
		agent.NewTextMessage(agent.RoleAssistant, "old answer"),
		agent.NewTextMessage(agent.RoleUser, "tool user"),
		assistant,
		result,
		agent.NewTextMessage(agent.RoleAssistant, "final"),
	)

	plan, err := PrepareCompaction(branch, CompactionOptions{KeepRecentTokens: 0})
	if err != nil {
		t.Fatalf("PrepareCompaction() error = %v", err)
	}
	if plan == nil {
		t.Fatal("PrepareCompaction() plan = nil")
	}
	if len(plan.RetainedTail) != 4 || plan.RetainedTail[2].ToolResult == nil {
		t.Fatalf("RetainedTail = %#v", plan.RetainedTail)
	}
}

func TestPrepareCompactionDoesNotTreatNonUserFirstMessageAsTurn(t *testing.T) {
	t.Parallel()

	branch := compactionMessageBranch(t,
		agent.NewTextMessage(agent.RoleAssistant, "orphan assistant"),
		agent.NewTextMessage(agent.RoleUser, "u1"),
		agent.NewTextMessage(agent.RoleAssistant, "a1"),
		agent.NewTextMessage(agent.RoleUser, "u2"),
		agent.NewTextMessage(agent.RoleAssistant, "a2"),
	)
	plan, err := PrepareCompaction(branch, CompactionOptions{KeepRecentTokens: 0})
	if err != nil {
		t.Fatalf("PrepareCompaction() error = %v", err)
	}
	if plan != nil {
		t.Fatalf("PrepareCompaction() plan = %#v, want nil", plan)
	}
}

func TestPrepareCompactionUsesLatestSummaryAndRetainedTail(t *testing.T) {
	t.Parallel()

	u1 := agent.NewTextMessage(agent.RoleUser, "discarded user")
	a1 := agent.NewTextMessage(agent.RoleAssistant, "discarded answer")
	u2 := agent.NewTextMessage(agent.RoleUser, "retained user")
	a2 := agent.NewTextMessage(agent.RoleAssistant, "retained answer")
	u3 := agent.NewTextMessage(agent.RoleUser, "new user")
	a3 := agent.NewTextMessage(agent.RoleAssistant, "new answer")
	log := []LogItem{
		reducerEntryItem(1, MainLane, "u1", "", u1),
		reducerEntryItem(2, MainLane, "a1", "u1", a1),
		reducerCompactionItem(3, MainLane, "c1", "a1", CompactionData{
			Summary:      "previous summary",
			RetainedTail: []agent.Message{u2, a2},
			TokensBefore: 20,
		}),
		reducerEntryItem(4, MainLane, "u3", "c1", u3),
		reducerEntryItem(5, MainLane, "a3", "u3", a3),
	}
	state, err := Reduce(log)
	if err != nil {
		t.Fatalf("Reduce() error = %v", err)
	}
	branch, err := state.Branch(MainLane)
	if err != nil {
		t.Fatalf("Branch() error = %v", err)
	}

	plan, err := PrepareCompaction(branch, CompactionOptions{
		KeepRecentTokens: 0,
		EstimateTokens:   func(agent.Message) int64 { return 2 },
	})
	if err != nil {
		t.Fatalf("PrepareCompaction() error = %v", err)
	}
	if plan == nil {
		t.Fatal("PrepareCompaction() plan = nil")
	}
	if plan.PreviousSummary != "previous summary" {
		t.Fatalf("PreviousSummary = %q", plan.PreviousSummary)
	}
	assertMessageTexts(t, plan.MessagesToSummarize, "retained user", "retained answer")
	assertMessageTexts(t, plan.RetainedTail, "new user", "new answer")
	if plan.TokensBefore != 10 {
		t.Fatalf("TokensBefore = %d, want 10 (summary plus four messages)", plan.TokensBefore)
	}
}

func TestRepeatedCompactionReplacesPreviousSummaryAndTail(t *testing.T) {
	t.Parallel()

	storage, err := NewMemoryStorage(Header{ID: "repeated-compaction"})
	if err != nil {
		t.Fatalf("NewMemoryStorage() error = %v", err)
	}
	for index, text := range []string{"u1", "a1", "u2", "a2", "u3", "a3"} {
		role := agent.RoleUser
		if index%2 == 1 {
			role = agent.RoleAssistant
		}
		message := agent.NewTextMessage(role, text)
		if _, err := storage.AppendEntry(MainLane, NewEntry{Type: EntryTypeMessage, ID: "first-" + text, Message: &message}); err != nil {
			t.Fatalf("AppendEntry(%s) error = %v", text, err)
		}
	}
	state, err := Reduce(storage.Log())
	if err != nil {
		t.Fatalf("first Reduce() error = %v", err)
	}
	branch, err := state.Branch(MainLane)
	if err != nil {
		t.Fatalf("first Branch() error = %v", err)
	}
	firstPlan, err := PrepareCompaction(branch, CompactionOptions{KeepRecentTokens: 0})
	if err != nil || firstPlan == nil {
		t.Fatalf("first PrepareCompaction() plan=%#v error=%v", firstPlan, err)
	}
	firstData, err := Compact(context.Background(), *firstPlan, func(context.Context, SummaryRequest) (SummaryResult, error) {
		return SummaryResult{Summary: "summary one"}, nil
	})
	if err != nil {
		t.Fatalf("first Compact() error = %v", err)
	}
	if _, err := storage.AppendEntry(MainLane, NewEntry{Type: EntryTypeCompaction, ID: "compaction-1", Compaction: &firstData}); err != nil {
		t.Fatalf("append first compaction error = %v", err)
	}
	for index, text := range []string{"u4", "a4"} {
		role := agent.RoleUser
		if index == 1 {
			role = agent.RoleAssistant
		}
		message := agent.NewTextMessage(role, text)
		if _, err := storage.AppendEntry(MainLane, NewEntry{Type: EntryTypeMessage, ID: "second-" + text, Message: &message}); err != nil {
			t.Fatalf("AppendEntry(%s) error = %v", text, err)
		}
	}

	state, err = Reduce(storage.Log())
	if err != nil {
		t.Fatalf("second Reduce() error = %v", err)
	}
	branch, err = state.Branch(MainLane)
	if err != nil {
		t.Fatalf("second Branch() error = %v", err)
	}
	secondPlan, err := PrepareCompaction(branch, CompactionOptions{KeepRecentTokens: 0})
	if err != nil || secondPlan == nil {
		t.Fatalf("second PrepareCompaction() plan=%#v error=%v", secondPlan, err)
	}
	if secondPlan.PreviousSummary != "summary one" {
		t.Fatalf("second PreviousSummary = %q", secondPlan.PreviousSummary)
	}
	assertMessageTexts(t, secondPlan.MessagesToSummarize, "u3", "a3")
	secondData, err := Compact(context.Background(), *secondPlan, func(_ context.Context, request SummaryRequest) (SummaryResult, error) {
		if request.PreviousSummary != "summary one" {
			t.Fatalf("summarizer PreviousSummary = %q", request.PreviousSummary)
		}
		return SummaryResult{Summary: "summary two"}, nil
	})
	if err != nil {
		t.Fatalf("second Compact() error = %v", err)
	}
	if _, err := storage.AppendEntry(MainLane, NewEntry{Type: EntryTypeCompaction, ID: "compaction-2", Compaction: &secondData}); err != nil {
		t.Fatalf("append second compaction error = %v", err)
	}
	finalState, err := Reduce(storage.Log())
	if err != nil {
		t.Fatalf("final Reduce() error = %v", err)
	}
	finalContext, err := finalState.Context(MainLane)
	if err != nil {
		t.Fatalf("final Context() error = %v", err)
	}
	if finalContext.Summary != "summary two" {
		t.Fatalf("final summary = %q", finalContext.Summary)
	}
	assertMessageTexts(t, finalContext.Messages, "u4", "a4")
}

func TestPrepareCompactionReturnsNilWithoutDiscardableCompleteTurn(t *testing.T) {
	t.Parallel()

	branch := compactionMessageBranch(t,
		agent.NewTextMessage(agent.RoleUser, "only user"),
		agent.NewTextMessage(agent.RoleAssistant, "only answer"),
	)
	plan, err := PrepareCompaction(branch, CompactionOptions{KeepRecentTokens: 0})
	if err != nil {
		t.Fatalf("PrepareCompaction() error = %v", err)
	}
	if plan != nil {
		t.Fatalf("PrepareCompaction() plan = %#v, want nil", plan)
	}
}

func TestCompactReturnsStorageIndependentDataAndDetachedValues(t *testing.T) {
	t.Parallel()

	plan := CompactionPlan{
		PreviousSummary: "previous",
		MessagesToSummarize: []agent.Message{
			agent.NewTextMessage(agent.RoleUser, "old user"),
			agent.NewTextMessage(agent.RoleAssistant, "old answer"),
		},
		RetainedTail: []agent.Message{
			agent.NewTextMessage(agent.RoleUser, "tail user"),
			agent.NewTextMessage(agent.RoleAssistant, "tail answer"),
		},
		TokensBefore: 77,
	}
	typedMetadata := map[string]string{"value": "original"}
	plan.MessagesToSummarize[0].Metadata = map[string]any{"typed": typedMetadata}
	details := json.RawMessage(`{"artifact":"index"}`)
	data, err := Compact(context.Background(), plan, func(_ context.Context, request SummaryRequest) (SummaryResult, error) {
		if request.PreviousSummary != "previous" {
			t.Fatalf("PreviousSummary = %q", request.PreviousSummary)
		}
		assertMessageTexts(t, request.Messages, "old user", "old answer")
		request.Messages[0].Parts[0].Text = "callback mutated"
		request.Messages[0].Metadata["typed"].(map[string]any)["value"] = "callback mutated"
		return SummaryResult{Summary: "updated", Details: details}, nil
	})
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if data.Summary != "updated" || data.TokensBefore != 77 {
		t.Fatalf("Compact() data = %#v", data)
	}
	assertMessageTexts(t, data.RetainedTail, "tail user", "tail answer")
	if string(data.Details) != string(details) {
		t.Fatalf("Details = %s", data.Details)
	}
	if plan.MessagesToSummarize[0].Parts[0].Text != "old user" {
		t.Fatalf("summarizer mutated plan: %#v", plan.MessagesToSummarize)
	}
	if typedMetadata["value"] != "original" {
		t.Fatalf("summarizer mutated typed plan metadata: %v", typedMetadata)
	}
	details[2] = 'X'
	if string(data.Details) != `{"artifact":"index"}` {
		t.Fatalf("Compact() aliased summarizer details: %s", data.Details)
	}
}

func TestCompactValidatesPlanAndSummary(t *testing.T) {
	t.Parallel()

	validPlan := CompactionPlan{
		MessagesToSummarize: []agent.Message{
			agent.NewTextMessage(agent.RoleUser, "old user"),
			agent.NewTextMessage(agent.RoleAssistant, "old answer"),
		},
		RetainedTail: []agent.Message{
			agent.NewTextMessage(agent.RoleUser, "new user"),
			agent.NewTextMessage(agent.RoleAssistant, "new answer"),
		},
	}
	tests := []struct {
		name       string
		plan       CompactionPlan
		summarizer Summarizer
	}{
		{name: "nil summarizer", plan: validPlan},
		{
			name:       "empty prefix",
			plan:       CompactionPlan{},
			summarizer: func(context.Context, SummaryRequest) (SummaryResult, error) { return SummaryResult{Summary: "x"}, nil },
		},
		{
			name:       "empty summary",
			plan:       validPlan,
			summarizer: func(context.Context, SummaryRequest) (SummaryResult, error) { return SummaryResult{}, nil },
		},
		{
			name: "invalid details",
			plan: validPlan,
			summarizer: func(context.Context, SummaryRequest) (SummaryResult, error) {
				return SummaryResult{Summary: "summary", Details: json.RawMessage(`{"broken"`)}, nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Compact(context.Background(), test.plan, test.summarizer)
			if !errors.Is(err, &Error{Code: ErrorInvalidEntry}) {
				t.Fatalf("Compact() error = %v, want invalid_entry", err)
			}
		})
	}
}

func TestCompactRejectsForgedUnsafeCutBeforeCallingSummarizer(t *testing.T) {
	t.Parallel()

	assistant := agent.NewTextMessage(agent.RoleAssistant, "calling")
	assistant.ToolCalls = []agent.ToolCall{{ID: "call-1", Name: "lookup"}}
	result := agent.NewToolResultMessage(assistant.ToolCalls[0], agent.ToolResult{}, false)
	called := false
	_, err := Compact(context.Background(), CompactionPlan{
		MessagesToSummarize: []agent.Message{
			agent.NewTextMessage(agent.RoleUser, "u1"),
			assistant,
		},
		RetainedTail: []agent.Message{
			result,
			agent.NewTextMessage(agent.RoleAssistant, "a1"),
			agent.NewTextMessage(agent.RoleUser, "u2"),
			agent.NewTextMessage(agent.RoleAssistant, "a2"),
		},
	}, func(context.Context, SummaryRequest) (SummaryResult, error) {
		called = true
		return SummaryResult{Summary: "should not run"}, nil
	})
	if !errors.Is(err, &Error{Code: ErrorInvalidEntry}) {
		t.Fatalf("Compact() error = %v, want invalid_entry", err)
	}
	if called {
		t.Fatal("Compact() called summarizer for forged unsafe cut")
	}
}

func TestCompactHonorsCancellationBeforeAndAfterSummarizer(t *testing.T) {
	plan := CompactionPlan{
		MessagesToSummarize: []agent.Message{
			agent.NewUserTextMessage("old"),
			agent.NewTextMessage(agent.RoleAssistant, "old answer"),
		},
		RetainedTail: []agent.Message{
			agent.NewUserTextMessage("new"),
			agent.NewTextMessage(agent.RoleAssistant, "new answer"),
		},
		TokensBefore: 4,
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	if _, err := Compact(canceled, plan, func(context.Context, SummaryRequest) (SummaryResult, error) {
		called = true
		return SummaryResult{Summary: "unused"}, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Compact pre-canceled error = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("summarizer was called for a pre-canceled compaction")
	}

	during, cancelDuring := context.WithCancel(context.Background())
	if _, err := Compact(during, plan, func(context.Context, SummaryRequest) (SummaryResult, error) {
		cancelDuring()
		return SummaryResult{Summary: "must not be committed"}, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Compact canceled-after-summary error = %v, want context.Canceled", err)
	}
}

func TestCompactionRejectsNonDurableInputsBeforeCallbacks(t *testing.T) {
	branch := compactionMessageBranch(t,
		agent.NewUserTextMessage("old"),
		agent.NewTextMessage(agent.RoleAssistant, "old answer"),
		agent.NewUserTextMessage("new"),
		agent.NewTextMessage(agent.RoleAssistant, "new answer"),
	)
	branch[0].Message.Metadata = map[string]any{"callback": func() {}}
	estimatorCalled := false
	if _, err := PrepareCompaction(branch, CompactionOptions{EstimateTokens: func(agent.Message) int64 {
		estimatorCalled = true
		return 1
	}}); !errors.Is(err, &Error{Code: ErrorInvalidEntry}) {
		t.Fatalf("PrepareCompaction non-durable error = %v, want invalid_entry", err)
	}
	if estimatorCalled {
		t.Fatal("estimator was called for a non-durable branch")
	}

	plan := CompactionPlan{
		MessagesToSummarize: []agent.Message{
			agent.NewUserTextMessage("old"),
			agent.NewTextMessage(agent.RoleAssistant, "old answer"),
		},
		RetainedTail: []agent.Message{
			agent.NewUserTextMessage("new"),
			agent.NewTextMessage(agent.RoleAssistant, "new answer"),
		},
	}
	plan.RetainedTail[0].Metadata = map[string]any{"callback": func() {}}
	summarizerCalled := false
	if _, err := Compact(context.Background(), plan, func(context.Context, SummaryRequest) (SummaryResult, error) {
		summarizerCalled = true
		return SummaryResult{Summary: "unused"}, nil
	}); !errors.Is(err, &Error{Code: ErrorInvalidEntry}) {
		t.Fatalf("Compact non-durable error = %v, want invalid_entry", err)
	}
	if summarizerCalled {
		t.Fatal("summarizer was called for a non-durable plan")
	}
}

func TestEstimateMessageTokensIsDeterministicAndPositive(t *testing.T) {
	t.Parallel()

	message := agent.NewTextMessage(agent.RoleAssistant, "12345678")
	message.ToolCalls = []agent.ToolCall{{ID: "id", Name: "tool", Arguments: json.RawMessage(`{"x":1}`)}}
	first := EstimateMessageTokens(message)
	second := EstimateMessageTokens(message)
	if first <= 0 || first != second {
		t.Fatalf("EstimateMessageTokens() = %d then %d", first, second)
	}

	toolResult := agent.NewToolResultMessage(
		agent.ToolCall{ID: "call", Name: "tool"},
		agent.ToolResult{
			Content: []agent.Part{{Type: agent.PartTypeText, Text: "123456789012"}},
			Details: map[string]any{"artifact": "details"},
		},
		false,
	)
	if got := EstimateMessageTokens(toolResult); got <= 3 {
		t.Fatalf("tool result content/details were not included in estimate: %d", got)
	}
	canonicalToolResult := agent.NewToolResultMessage(
		agent.ToolCall{ID: "call", Name: "tool"},
		agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "123456789012"}}},
		false,
	)
	if got := EstimateMessageTokens(canonicalToolResult); got != 4 {
		t.Fatalf("canonical tool result content was double counted: got %d, want 4", got)
	}
}

func TestPrepareCompactionRejectsTokenEstimateOverflow(t *testing.T) {
	branch := compactionMessageBranch(t,
		agent.NewUserTextMessage("old"),
		agent.NewTextMessage(agent.RoleAssistant, "old answer"),
		agent.NewUserTextMessage("new"),
		agent.NewTextMessage(agent.RoleAssistant, "new answer"),
	)
	_, err := PrepareCompaction(branch, CompactionOptions{
		EstimateTokens: func(agent.Message) int64 { return maxTokenCount },
	})
	if !errors.Is(err, &Error{Code: ErrorInvalidEntry}) {
		t.Fatalf("PrepareCompaction overflow error = %v, want invalid_entry", err)
	}
}

func compactionMessageBranch(t *testing.T, messages ...agent.Message) []Entry {
	t.Helper()
	log := make([]LogItem, 0, len(messages))
	parent := ""
	for index, message := range messages {
		id := "message-" + string(rune('a'+index))
		seq := uint64(index + 1)
		log = append(log, reducerEntryItem(seq, MainLane, id, parent, message))
		parent = id
	}
	state, err := Reduce(log)
	if err != nil {
		t.Fatalf("Reduce() error = %v", err)
	}
	branch, err := state.Branch(MainLane)
	if err != nil {
		t.Fatalf("Branch() error = %v", err)
	}
	return branch
}
