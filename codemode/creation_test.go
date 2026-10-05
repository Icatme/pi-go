package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Icatme/pi-go/codemode/internal/assets"
)

func TestSandboxCompilesEmbeddedModule(t *testing.T) {
	// This test must remain serial: assets.WASM is shared by sandbox creation.
	original := assets.WASM
	t.Cleanup(func() { assets.WASM = original })

	t.Run("valid module without a repeated digest check", func(t *testing.T) {
		// Append an inert custom section named "audit". Asset tests independently
		// pin the shipped module, while runtime creation only needs valid WASM.
		assets.WASM = append(append([]byte(nil), original...), 0, 7, 5, 'a', 'u', 'd', 'i', 't', 0)
		sandbox := sandboxForTest(t, DefaultConfig())
		result, err := sandbox.Run(t.Context(), "text(3);", RunOptions{})
		if err != nil || textOutput(result) != "3" {
			t.Fatalf("valid module execution: result=%+v err=%v", result, err)
		}
	})

	t.Run("invalid module rejected by compiler", func(t *testing.T) {
		assets.WASM = []byte("invalid wasm")
		sandbox, err := NewSandbox(t.Context(), DefaultConfig())
		if sandbox != nil {
			_ = sandbox.Close(t.Context())
			t.Fatal("invalid module produced a sandbox")
		}
		if err == nil || !strings.Contains(err.Error(), "invalid magic number") {
			t.Fatalf("expected WASM compiler validation error, got %v", err)
		}
	})
}

func TestConcurrentSandboxCreation(t *testing.T) {
	const count = 8
	start := make(chan struct{})
	results := make(chan error, count)
	for range count {
		go func() {
			<-start
			s, err := NewSandbox(t.Context(), DefaultConfig())
			if err == nil {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				_, runErr := s.Run(ctx, "text(3);", RunOptions{})
				cancel()
				closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
				err = errors.Join(runErr, s.Close(closeCtx))
				closeCancel()
			}
			results <- err
		}()
	}
	close(start)
	for range count {
		select {
		case err := <-results:
			if err != nil {
				t.Error(err)
			}
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}
}

func TestSandboxRuntimeIsolation(t *testing.T) {
	first := sandboxForTest(t, DefaultConfig())
	second := sandboxForTest(t, DefaultConfig())
	for _, test := range []struct {
		sandbox *Sandbox
		value   string
	}{{first, "first"}, {second, "second"}} {
		result, err := test.sandbox.Run(t.Context(), `text(await tools.owner({})); store("private", true);`, RunOptions{Tools: []Tool{{Name: "owner", Invoke: func(context.Context, HostCall) (json.RawMessage, error) {
			return json.Marshal(test.value)
		}}}})
		if err != nil || textOutput(result) != test.value {
			t.Fatalf("runtime reused another host binding: result=%+v err=%v", result, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started, stopped := make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := first.Run(ctx, `await tools.started({}); while (true) {}`, RunOptions{Tools: []Tool{{Name: "started", Invoke: func(context.Context, HostCall) (json.RawMessage, error) {
			close(started)
			return json.RawMessage(`null`), nil
		}}}})
		stopped <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first runtime never entered its script")
	}
	cancel()
	select {
	case err := <-stopped:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled runtime execution: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first runtime ignored cancellation")
	}
	if err := first.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	third := sandboxForTest(t, DefaultConfig())
	for _, sandbox := range []*Sandbox{second, third} {
		result, err := sandbox.Run(t.Context(), `text(load("private") === undefined);`, RunOptions{})
		if err != nil || textOutput(result) != "true" {
			t.Fatalf("closing another runtime invalidated execution or leaked state: result=%+v err=%v", result, err)
		}
	}
}

func BenchmarkSandboxCreation(b *testing.B) {
	for b.Loop() {
		sandbox, err := NewSandbox(b.Context(), DefaultConfig())
		if err != nil {
			b.Fatal(err)
		}
		if err := sandbox.Close(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}
