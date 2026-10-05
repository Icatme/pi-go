package checkpoint

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

// Run unchanged against the baseline: includes canonicalizing the actual final
// arguments, and the binding work performed even when no approval is required.
func BenchmarkApprovalGateAllow(b *testing.B) {
	for _, size := range []int{64, 64 << 10} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			args := map[string]any{"text": strings.Repeat("x", size)}
			raw, err := json.Marshal(args)
			if err != nil {
				b.Fatal(err)
			}
			input := agent.BeforeToolCallContext{ToolCall: agent.ToolCall{ID: "call", Name: "tool", Arguments: raw}, Args: args}
			gate := newApprovalGate(nil, nil, "v1")
			b.ReportAllocs()
			for b.Loop() {
				result, err := gate.evaluate(context.Background(), input)
				if err != nil || result.Action != agent.ToolGateActionAllow {
					b.Fatalf("allow: %+v, %v", result, err)
				}
			}
		})
	}
}
