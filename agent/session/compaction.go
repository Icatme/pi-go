package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent"
)

// MessageTokenEstimator estimates the context tokens consumed by one message.
// It is intentionally provider-independent; applications may replace it with
// a model-aware estimator.
type MessageTokenEstimator func(agent.Message) int64

// CompactionOptions controls which complete recent turns remain verbatim.
type CompactionOptions struct {
	KeepRecentTokens int64
	EstimateTokens   MessageTokenEstimator
}

// CompactionPlan is a pure, storage-independent cut of the effective branch
// context. The summarized prefix and retained tail always meet at a complete
// turn boundary.
type CompactionPlan struct {
	PreviousSummary     string
	MessagesToSummarize []agent.Message
	RetainedTail        []agent.Message
	TokensBefore        int64
}

// SummaryRequest is passed to a caller-provided summarizer.
type SummaryRequest struct {
	PreviousSummary string
	Messages        []agent.Message
}

// SummaryResult is the provider-independent output of a summarizer. Details
// may contain application-owned JSON metadata such as an artifact index.
type SummaryResult struct {
	Summary string
	Details json.RawMessage
}

// Summarizer produces a replacement summary for a compacted prefix.
type Summarizer func(context.Context, SummaryRequest) (SummaryResult, error)

const maxTokenCount = int64(^uint64(0) >> 1)

// PrepareCompaction selects a complete-turn cut point for branch. A nil plan
// means there is no non-empty, complete prefix that can be compacted while
// retaining at least the newest turn.
func PrepareCompaction(branch []Entry, options CompactionOptions) (*CompactionPlan, error) {
	if options.KeepRecentTokens < 0 {
		return nil, invalidCompaction("keep recent tokens must be non-negative")
	}
	normalizedBranch, err := cloneValidated(branch)
	if err != nil {
		return nil, invalidCompaction("branch is not durable JSON: %v", err)
	}
	estimator := options.EstimateTokens
	if estimator == nil {
		estimator = EstimateMessageTokens
	}

	effective := contextFromBranch(normalizedBranch)
	starts, err := completeTurnStarts(effective.Messages)
	if err != nil {
		return nil, err
	}
	if len(starts) < 2 {
		return nil, nil
	}

	tokensBefore, err := estimateContextTokens(effective, estimator)
	if err != nil {
		return nil, err
	}
	firstKept := starts[len(starts)-1]
	retainedTokens := int64(0)
	for turn := len(starts) - 1; turn >= 0; turn-- {
		start := starts[turn]
		end := len(effective.Messages)
		if turn+1 < len(starts) {
			end = starts[turn+1]
		}
		turnTokens, estimateErr := estimateMessages(effective.Messages[start:end], estimator)
		if estimateErr != nil {
			return nil, estimateErr
		}
		if turn < len(starts)-1 && retainedTokens >= options.KeepRecentTokens {
			break
		}
		firstKept = start
		if retainedTokens > maxTokenCount-turnTokens {
			return nil, invalidCompaction("retained token estimate overflowed")
		}
		retainedTokens += turnTokens
	}
	if firstKept == 0 {
		return nil, nil
	}

	return &CompactionPlan{
		PreviousSummary:     effective.Summary,
		MessagesToSummarize: cloneMessages(effective.Messages[:firstKept]),
		RetainedTail:        cloneMessages(effective.Messages[firstKept:]),
		TokensBefore:        tokensBefore,
	}, nil
}

// Compact invokes summarize and returns data that a Session or Storage layer
// can wrap in a NewEntry. It does not invent an entry id, sequence, parent,
// lane, or timestamp.
func Compact(ctx context.Context, plan CompactionPlan, summarize Summarizer) (CompactionData, error) {
	if summarize == nil {
		return CompactionData{}, invalidCompaction("summarizer is required")
	}
	if len(plan.MessagesToSummarize) == 0 {
		return CompactionData{}, invalidCompaction("compaction plan has no messages to summarize")
	}
	if plan.TokensBefore < 0 {
		return CompactionData{}, invalidCompaction("compaction plan has a negative token count")
	}
	normalizedPlan, err := cloneValidated(plan)
	if err != nil {
		return CompactionData{}, invalidCompaction("plan is not durable JSON: %v", err)
	}
	plan = normalizedPlan
	if err := validateCompactionCut(plan.MessagesToSummarize, plan.RetainedTail); err != nil {
		return CompactionData{}, err
	}
	if err := ctx.Err(); err != nil {
		return CompactionData{}, err
	}

	result, err := summarize(ctx, SummaryRequest{
		PreviousSummary: plan.PreviousSummary,
		Messages:        cloneMessages(plan.MessagesToSummarize),
	})
	if err != nil {
		return CompactionData{}, err
	}
	if err := ctx.Err(); err != nil {
		return CompactionData{}, err
	}
	if strings.TrimSpace(result.Summary) == "" {
		return CompactionData{}, invalidCompaction("summarizer returned an empty summary")
	}
	if len(result.Details) != 0 && !json.Valid(result.Details) {
		return CompactionData{}, invalidCompaction("summarizer returned invalid details JSON")
	}
	return CompactionData{
		Summary:      result.Summary,
		RetainedTail: cloneMessages(plan.RetainedTail),
		TokensBefore: plan.TokensBefore,
		Details:      cloneRawMessage(result.Details),
	}, nil
}

// EstimateMessageTokens provides a deterministic fallback estimate. It is not
// presented as provider billing usage; model-aware callers should replace it.
func EstimateMessageTokens(message agent.Message) int64 {
	characters := 0
	if message.ToolResult == nil {
		for _, part := range message.Parts {
			characters += estimatePartCharacters(part)
		}
	}
	for _, call := range message.ToolCalls {
		characters += utf8.RuneCountInString(call.Name)
		characters += utf8.RuneCount(call.Arguments)
	}
	if message.ToolResult != nil {
		characters += utf8.RuneCountInString(message.ToolResult.ToolName)
		for _, part := range message.ToolResult.Content {
			characters += estimatePartCharacters(part)
		}
		if message.ToolResult.Details != nil {
			if details, err := json.Marshal(message.ToolResult.Details); err == nil {
				characters += utf8.RuneCount(details)
			}
		}
	}
	if characters == 0 {
		return 1
	}
	return int64((characters + 3) / 4)
}

func estimatePartCharacters(part agent.Part) int {
	if part.Type == agent.PartTypeImage {
		// Image tokenization is provider-dependent. A fixed conservative
		// estimate avoids treating base64 byte length as text tokens.
		return 4800
	}
	return utf8.RuneCountInString(part.Text)
}

func completeTurnStarts(messages []agent.Message) ([]int, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	starts := []int{0}
	seenAssistant := false
	pending := newToolPairTracker()

	for index, message := range messages {
		switch message.Role {
		case agent.RoleUser:
			if pending.len() != 0 {
				return nil, invalidCompaction("user message at index %d crosses %d unmatched tool call(s)", index, pending.len())
			}
			if index > starts[len(starts)-1] && seenAssistant {
				starts = append(starts, index)
				seenAssistant = false
			}
		case agent.RoleAssistant:
			if pending.len() != 0 {
				return nil, invalidCompaction("assistant message at index %d crosses %d unmatched tool call(s)", index, pending.len())
			}
			seenAssistant = true
			for callIndex, call := range message.ToolCalls {
				if err := pending.add(call, index, callIndex); err != nil {
					return nil, err
				}
			}
		case agent.RoleTool:
			if message.ToolResult == nil {
				return nil, invalidCompaction("tool message at index %d has no tool result payload", index)
			}
			if err := pending.finish(*message.ToolResult, index); err != nil {
				return nil, err
			}
		}
	}
	if pending.len() != 0 {
		return nil, invalidCompaction("transcript ends with %d unmatched tool call(s)", pending.len())
	}
	if messages[0].Role != agent.RoleUser {
		return nil, nil
	}
	return starts, nil
}

func validateCompactionCut(prefix, tail []agent.Message) error {
	combined := make([]agent.Message, 0, len(prefix)+len(tail))
	combined = append(combined, cloneMessages(prefix)...)
	combined = append(combined, cloneMessages(tail)...)
	starts, err := completeTurnStarts(combined)
	if err != nil {
		return err
	}
	cut := len(prefix)
	for _, start := range starts {
		if start == cut && cut > 0 {
			return nil
		}
	}
	return invalidCompaction("plan cut at message %d is not a complete user turn boundary", cut)
}

type toolPairTracker struct {
	nextID      int
	byAlias     map[string]int
	aliasesByID map[int][]string
}

func newToolPairTracker() *toolPairTracker {
	return &toolPairTracker{
		byAlias:     make(map[string]int),
		aliasesByID: make(map[int][]string),
	}
}

func (t *toolPairTracker) len() int {
	return len(t.aliasesByID)
}

func (t *toolPairTracker) add(call agent.ToolCall, messageIndex, callIndex int) error {
	aliases := uniqueNonEmpty(call.ID, call.OriginalID)
	if len(aliases) == 0 {
		return invalidCompaction("assistant tool call %d at message %d has no id", callIndex, messageIndex)
	}
	for _, alias := range aliases {
		if _, exists := t.byAlias[alias]; exists {
			return invalidCompaction("assistant tool call %d at message %d repeats pending id %q", callIndex, messageIndex, alias)
		}
	}
	t.nextID++
	canonical := t.nextID
	t.aliasesByID[canonical] = aliases
	for _, alias := range aliases {
		t.byAlias[alias] = canonical
	}
	return nil
}

func (t *toolPairTracker) finish(result agent.ToolResultPayload, messageIndex int) error {
	aliases := uniqueNonEmpty(result.ToolCallID, result.OriginalToolCallID)
	if len(aliases) == 0 {
		return invalidCompaction("tool result at message %d has no tool call id", messageIndex)
	}
	canonical := 0
	for _, alias := range aliases {
		candidate, exists := t.byAlias[alias]
		if !exists {
			return invalidCompaction("tool result at message %d references unknown tool call id %q", messageIndex, alias)
		}
		if canonical != 0 && canonical != candidate {
			return invalidCompaction("tool result at message %d identifies multiple pending calls", messageIndex)
		}
		canonical = candidate
	}
	if canonical == 0 {
		return invalidCompaction("tool result at message %d does not match a pending tool call", messageIndex)
	}
	for _, alias := range t.aliasesByID[canonical] {
		delete(t.byAlias, alias)
	}
	delete(t.aliasesByID, canonical)
	return nil
}

func uniqueNonEmpty(values ...string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func estimateContextTokens(context Context, estimator MessageTokenEstimator) (int64, error) {
	tokens, err := estimateMessages(context.Messages, estimator)
	if err != nil {
		return 0, err
	}
	if context.Summary != "" {
		summaryTokens := estimator(agent.Message{
			Role:  agent.RoleCustom,
			Kind:  "session.compaction_summary",
			Parts: []agent.Part{{Type: agent.PartTypeText, Text: context.Summary}},
		})
		if summaryTokens < 0 {
			return 0, invalidCompaction("token estimator returned a negative value for the previous summary")
		}
		if tokens > maxTokenCount-summaryTokens {
			return 0, invalidCompaction("context token estimate overflowed")
		}
		tokens += summaryTokens
	}
	return tokens, nil
}

func estimateMessages(messages []agent.Message, estimator MessageTokenEstimator) (int64, error) {
	tokens := int64(0)
	for index, message := range messages {
		estimated := estimator(cloneMessage(message))
		if estimated < 0 {
			return 0, invalidCompaction("token estimator returned a negative value for message %d", index)
		}
		if tokens > maxTokenCount-estimated {
			return 0, invalidCompaction("token estimate overflowed at message %d", index)
		}
		tokens += estimated
	}
	return tokens, nil
}

func invalidCompaction(format string, args ...any) error {
	return &Error{Code: ErrorInvalidEntry, Message: fmt.Sprintf("invalid session compaction: "+format, args...)}
}
