package session

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
)

var storedLogCopyBenchmarkResult []LogItem

// BenchmarkStoredLogCopy compares both implementations in-process against the
// exact same validated, normalized state. Fixture construction and validation
// are excluded from the measured read cost. Mixed alternates custom/messages.
func BenchmarkStoredLogCopy(b *testing.B) {
	for _, profile := range []string{"custom", "message", "mixed"} {
		for _, count := range []int{100, 1000} {
			b.Run(fmt.Sprintf("%s/%d", profile, count), func(b *testing.B) {
				state := storedLogCopyBenchmarkState(b, profile, count)
				if !reflect.DeepEqual(state.logClone(), mustCloneJSON(state.log)) {
					b.Fatal("implementations disagree on fixture")
				}
				for _, implementation := range []struct {
					name string
					copy func() []LogItem
				}{
					{"original", func() []LogItem { return mustCloneJSON(state.log) }},
					{"optimized", state.logClone},
				} {
					b.Run(implementation.name, func(b *testing.B) {
						b.ReportAllocs()
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							result := implementation.copy()
							if len(result) != count {
								b.Fatal("incomplete log copy")
							}
							storedLogCopyBenchmarkResult = result
						}
					})
				}
			})
		}
	}
}

func storedLogCopyBenchmarkState(b *testing.B, profile string, count int) *logState {
	b.Helper()
	const timestamp = int64(1767225600000)
	state := newLogState(Header{Kind: HeaderKindSession, Version: CurrentFormatVersion, ID: "copy-benchmark", CreatedAt: timestamp})
	text := strings.Repeat("deterministic conversation content ", 16)
	raw, err := json.Marshal(map[string]any{"text": text, "nested": []any{map[string]any{"number": json.Number("9007199254740993123456789")}, nil, true}})
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < count; i++ {
		entry := Entry{ID: fmt.Sprintf("entry-%06d", i), Seq: uint64(i + 1), Lane: MainLane, Timestamp: timestamp}
		if i > 0 {
			entry.ParentID = fmt.Sprintf("entry-%06d", i-1)
		}
		if profile == "custom" || profile == "mixed" && i%2 == 0 {
			entry.Type = EntryTypeCustom
			entry.Custom = &CustomData{CustomType: "benchmark", Payload: raw}
		} else {
			entry.Type = EntryTypeMessage
			entry.Message = &agent.Message{
				Role: agent.RoleUser, Timestamp: time.UnixMilli(timestamp).UTC(),
				Parts:    []agent.Part{{Type: agent.PartTypeText, Text: text}},
				Metadata: map[string]any{"nested": []any{map[string]any{"number": json.Number("9007199254740993123456789")}, nil, true}},
			}
		}
		if err := state.apply(LogItem{Kind: LogItemEntry, Seq: entry.Seq, Entry: &entry}); err != nil {
			b.Fatal(err)
		}
	}
	return state
}
