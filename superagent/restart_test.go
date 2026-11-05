package superagent_test

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/superagent"
)

func TestProcessExitDuringWork(t *testing.T) {
	const helperState = "PIGO_SUPERAGENT_CRASH_TEST_STATE"
	event := superagent.Trigger{ID: "17", Source: "im", Thread: "incident", Kind: "request", Text: "Investigate build failure"}
	route := func(context.Context, superagent.Trigger, []superagent.TaskInfo) (superagent.Route, error) {
		return superagent.Route{Action: "new"}, nil
	}
	if path := os.Getenv(helperState); path != "" {
		entered := make(chan struct{})
		work := func(ctx context.Context, _ superagent.Job) (superagent.Outcome, error) {
			// The external write succeeds before a terminal journal record
			// exists. The parent kills this process at exactly that boundary.
			if err := os.WriteFile(path+".published", []byte("published"), 0o600); err != nil {
				return superagent.Outcome{}, err
			}
			close(entered)
			<-ctx.Done()
			return superagent.Outcome{}, ctx.Err()
		}
		s, err := superagent.Open(t.Context(), path, 1, route, work)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if _, err := s.Submit(event); err != nil {
			t.Fatal(err)
		}
		<-entered
		queued := event
		queued.ID = "18"
		if _, err := s.Submit(queued); err != nil {
			t.Fatal(err)
		}
		fmt.Println("worker entered with a queued delivery")
		if err := s.WaitIdle(t.Context()); err != nil {
			t.Fatal(err)
		}
		return
	}

	path := filepath.Join(t.TempDir(), "state.jsonl")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessExitDuringWork$")
	cmd.Env = append(os.Environ(), helperState+"="+path)
	cmd.Stderr = os.Stderr
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(output)
	entered := lines.Scan() && lines.Text() == "worker entered with a queued delivery"
	killErr := cmd.Process.Kill()
	waitErr := cmd.Wait()
	if !entered || killErr != nil || waitErr == nil {
		t.Fatalf("did not interrupt a running worker: entered=%t kill=%v exit=%v", entered, killErr, waitErr)
	}
	published, err := os.ReadFile(path + ".published")
	if err != nil || string(published) != "published" {
		t.Fatalf("expected the external write to survive: data=%q err=%v", published, err)
	}

	var executions atomic.Int32
	s, err := superagent.Open(ctx, path, 1, route, func(context.Context, superagent.Job) (superagent.Outcome, error) {
		executions.Add(1)
		return superagent.Outcome{Summary: "investigated"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	accepted, err := s.Submit(event)
	if err != nil || accepted {
		t.Fatalf("duplicate accepted=%t err=%v", accepted, err)
	}
	if err := s.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	tasks := s.Status().Tasks
	if len(tasks) != 1 || tasks[0].Status != "needs_review" || tasks[0].ReviewSource != event.Source || tasks[0].ReviewEventID != event.ID || executions.Load() != 0 {
		t.Fatalf("recovery tasks=%+v executions=%d", tasks, executions.Load())
	}
	reports := s.Status().Reports
	if len(reports) != 2 || reports[0].Status != "interrupted" || !reports[0].NeedsReview || len(reports[0].Receipts) != 0 || reports[1].Status != "blocked" {
		t.Fatal(reports)
	}
	other := event
	other.ID, other.Thread = "19", "another thread"
	if _, err := s.Submit(other); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitIdle(ctx); err != nil || executions.Load() != 1 {
		t.Fatalf("other task blocked: err=%v calls=%d", err, executions.Load())
	}
}
