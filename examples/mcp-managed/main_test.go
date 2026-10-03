package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

func TestManagedProcessPhase(t *testing.T) {
	path := os.Getenv("PIGO_MANAGED_SESSION")
	if path == "" {
		return
	}
	if err := runWorkflow(path, os.Getenv("PIGO_MANAGED_PHASE")); err != nil {
		t.Fatal(err)
	}
}

func TestManagedSessionAcrossProcessesAndCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	for _, phase := range []string{"save", "resume"} {
		ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManagedProcessPhase$", "-test.v")
		cmd.Env = append(os.Environ(), "PIGO_MANAGED_SESSION="+path, "PIGO_MANAGED_PHASE="+phase)
		output, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("%s: %v\n%s", phase, err, output)
		}
		if phase == "resume" && !strings.Contains(string(output), `"resumed":true`) {
			t.Fatalf("missing restart evidence: %s", output)
		}
	}
}
