package agent

import (
	"errors"
	"testing"
)

func TestEngineReceiptBoundaryPreservesNilContext(t *testing.T) {
	engine := NewEngine()
	tests := []struct {
		name string
		run  func() (*AgentSnapshot, error)
		want error
	}{
		{"Run", func() (*AgentSnapshot, error) {
			return engine.Run(nil, AgentDefinition{}, nil, nil, nil)
		}, ErrNoPromptMessages},
		{"RunWithHooks", func() (*AgentSnapshot, error) {
			return engine.RunWithHooks(nil, AgentDefinition{}, nil, nil, nil, LoopHooks{})
		}, ErrNoPromptMessages},
		{"Continue", func() (*AgentSnapshot, error) {
			return engine.Continue(nil, AgentDefinition{}, nil, nil)
		}, ErrNoMessagesToContinue},
		{"ContinueWithHooks", func() (*AgentSnapshot, error) {
			return engine.ContinueWithHooks(nil, AgentDefinition{}, nil, nil, LoopHooks{})
		}, ErrNoMessagesToContinue},
		{"ResumePendingToolCallsWithHooks", func() (*AgentSnapshot, error) {
			return engine.ResumePendingToolCallsWithHooks(nil, AgentDefinition{}, nil, nil, LoopHooks{})
		}, ErrToolGateRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("nil context panicked instead of returning %v: %v", test.want, p)
				}
			}()
			next, err := test.run()
			if next != nil || !errors.Is(err, test.want) {
				t.Fatalf("snapshot=%+v err=%v, want %v", next, err, test.want)
			}
		})
	}
}
