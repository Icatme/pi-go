package agent

import (
	"context"
	"fmt"
	"reflect"
	"sync"
)

// ToolInvocation owns trusted resources for one runtime invocation. It is not
// serialized, shared by sessions, or restored from a snapshot. The runtime
// grants it to container executors; scripts and leaf tools cannot choose it.
type ToolInvocation struct {
	mu     sync.Mutex
	values map[any]any
}

// LoadOrCreate retains one resource per comparable host-owned key. Factories
// run once under the holder lock and must not recursively access this holder.
// A failed factory does not populate the key.
func (i *ToolInvocation) LoadOrCreate(key any, create func() (any, error)) (any, error) {
	if i == nil {
		return nil, fmt.Errorf("agent: tool invocation is unavailable")
	}
	if key == nil || !reflect.TypeOf(key).Comparable() || create == nil {
		return nil, fmt.Errorf("agent: invocation resource requires a comparable key and factory")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if value, found := i.values[key]; found {
		return value, nil
	}
	value, err := create()
	if err != nil {
		return nil, err
	}
	if i.values == nil {
		i.values = make(map[any]any)
	}
	i.values[key] = value
	return value, nil
}

type toolInvocationContextKey struct{}

func newToolInvocationContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, toolInvocationContextKey{}, &ToolInvocation{})
}

func toolInvocationFromContext(ctx context.Context) *ToolInvocation {
	invocation, _ := ctx.Value(toolInvocationContextKey{}).(*ToolInvocation)
	return invocation
}
