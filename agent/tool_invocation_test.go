package agent

import (
	"errors"
	"testing"
)

func TestToolInvocationRejectsDynamicallyIncomparableKeys(t *testing.T) {
	type wrapper struct{ Value any }
	for _, key := range []any{
		nil,
		[]int{1},
		map[string]int{"x": 1},
		func() {},
		wrapper{Value: []int{1}},
		wrapper{Value: map[string]int{"x": 1}},
		wrapper{Value: func() {}},
		[1]any{[]int{1}},
		wrapper{Value: [1]any{wrapper{Value: map[string]int{}}}},
	} {
		invocation := &ToolInvocation{}
		called := false
		value, err := invocation.LoadOrCreate(key, func() (any, error) { called = true; return "unexpected", nil })
		if err == nil || value != nil || called {
			t.Fatalf("invalid key reached factory: %T value=%v err=%v called=%v", key, value, err, called)
		}
		// Rejection must not poison the holder or retain a partial entry.
		value, err = invocation.LoadOrCreate("valid", func() (any, error) { return "ok", nil })
		if err != nil || value != "ok" {
			t.Fatalf("holder unusable after rejection: %v %v", value, err)
		}
	}
}

func TestToolInvocationComparableNestedKeysCacheOnce(t *testing.T) {
	type wrapper struct{ Value any }
	var nilPointer *int
	for _, key := range []any{"key", wrapper{Value: "nested"}, [2]any{1, "x"}, wrapper{Value: nil}, nilPointer} {
		invocation := &ToolInvocation{}
		count := 0
		factory := func() (any, error) { count++; return "resource", nil }
		for i := 0; i < 2; i++ {
			value, err := invocation.LoadOrCreate(key, factory)
			if err != nil || value != "resource" {
				t.Fatalf("valid key rejected: %T %v %v", key, value, err)
			}
		}
		if count != 1 {
			t.Fatalf("factory ran %d times", count)
		}
	}
}

func TestToolInvocationFailedFactoryDoesNotPopulateKey(t *testing.T) {
	invocation := &ToolInvocation{}
	failure := errors.New("fixture failure")
	if value, err := invocation.LoadOrCreate("key", func() (any, error) { return "discard", failure }); value != nil || !errors.Is(err, failure) {
		t.Fatalf("factory failure changed: %v %v", value, err)
	}
	value, err := invocation.LoadOrCreate("key", func() (any, error) { return "retry", nil })
	if err != nil || value != "retry" {
		t.Fatalf("failed value was cached: %v %v", value, err)
	}
}
