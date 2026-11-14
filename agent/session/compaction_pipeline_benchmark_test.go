package session

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Icatme/pi-go/agent"
	"strings"
	"testing"
)

func BenchmarkCompactionPipeline(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20, 4 << 20} {
		raw, _ := json.Marshal(map[string]string{"content": strings.Repeat("x", size)})
		call := agent.Message{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "one", Name: "write", Arguments: raw}}}
		result := agent.NewToolResultMessage(call.ToolCalls[0], agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "ok"}}}, false)
		messages := []agent.Message{agent.NewUserTextMessage("old"), call, result, agent.NewUserTextMessage("new")}
		branch := make([]Entry, len(messages))
		for i := range messages {
			branch[i] = Entry{Type: EntryTypeMessage, Message: &messages[i]}
		}
		options := CompactionOptions{EstimateTokens: func(agent.Message) int64 { return 1 }}
		plan, err := PrepareCompaction(branch, options)
		if err != nil || plan == nil {
			b.Fatalf("plan %v", err)
		}
		b.Run(fmt.Sprintf("Prepare/%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				p, e := PrepareCompaction(branch, options)
				if e != nil || p == nil || len(p.MessagesToSummarize) != 3 {
					b.Fatal(e)
				}
			}
		})
		b.Run(fmt.Sprintf("Compact/%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				d, e := Compact(context.Background(), *plan, func(_ context.Context, r SummaryRequest) (SummaryResult, error) {
					if len(r.Messages) != 3 {
						b.Fatal("missing prefix")
					}
					return SummaryResult{Summary: "summary"}, nil
				})
				if e != nil || len(d.RetainedTail) != 1 {
					b.Fatal(e)
				}
			}
		})
	}
}
