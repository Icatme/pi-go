package codemode

import (
	"context"
	"errors"
	"testing"
	"time"
)

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
