package superagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestFailureCauseIsForHostOnly(t *testing.T) {
	const marker = "SYNTHETIC_PRIVATE_DIAGNOSTIC"
	cause := errors.New(marker)
	path := filepath.Join(t.TempDir(), "state.jsonl")
	route := func(_ context.Context, event Trigger, _ []TaskInfo) (Route, error) {
		if event.ID == "router" {
			return Route{}, fmt.Errorf("router failed: %w", cause)
		}
		return Route{Action: "new"}, nil
	}
	worker := func(context.Context, Job) (Outcome, error) {
		return Outcome{}, fmt.Errorf("executor failed: %w", cause)
	}
	a, err := Open(t.Context(), path, 1, route, worker)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, id := range []string{"router", "worker"} {
		submit(t, a, Trigger{ID: id, Source: "im", Thread: id, Kind: "request", Text: "investigate"})
	}
	waitIdle(t, a)
	for _, id := range []string{"router", "worker"} {
		if err := a.FailureCause("im", id); !errors.Is(err, cause) {
			t.Fatalf("%s cause=%v", id, err)
		}
	}
	reports, err := json.Marshal(a.ReportsSince(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(reports, []byte(marker)) || bytes.Contains(journal, []byte(marker)) {
		t.Fatal("private error was published")
	}
}
