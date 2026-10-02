package main

import "testing"

func TestLocalManagedWorkflowClosesAfterConcurrentRequests(t *testing.T) {
	const workflows = 8
	start := make(chan struct{})
	results := make(chan error, workflows)
	for range workflows {
		go func() {
			<-start
			results <- run()
		}()
	}
	close(start)
	for range workflows {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
}
