package superagent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/session"
)

func TestPiWorkerProviderAffinitySurvivesReopenAndSeparatesJournals(t *testing.T) {
	var used []string
	def := agent.AgentDefinition{Model: agent.StreamFunc(func(_ context.Context, r agent.ModelRequest) (agent.AssistantStream, error) {
		used = append(used, r.SessionID)
		return offlineAnswer("done"), nil
	})}
	worker, err := PiWorker(def)
	if err != nil {
		t.Fatal(err)
	}
	run := func(path, id, thread string) {
		t.Helper()
		a, err := Open(t.Context(), path, 1, func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil }, worker)
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		submit(t, a, Trigger{Source: "in", ID: id, Thread: thread, Kind: "request", Text: "work"})
		waitIdle(t, a)
		reports := a.Status().Reports
		last := reports[len(reports)-1]
		snapshot, ok, err := a.TaskSnapshot(last.TaskID)
		if last.Status != "completed" || err != nil || !ok || snapshot.SessionID != last.TaskID {
			t.Fatalf("report=%+v local snapshot owner=%q err=%v", last, snapshot.SessionID, err)
		}
	}
	a, b := filepath.Join(t.TempDir(), "one.jsonl"), filepath.Join(t.TempDir(), "two.jsonl")
	run(a, "1", "thread")
	run(a, "2", "thread")
	run(a, "3", "new-thread")
	run(b, "1", "thread")
	if len(used) != 4 || used[0] == "" || used[0] != used[1] || used[0] == used[2] || used[0] == used[3] || used[2] == used[3] {
		t.Fatalf("provider identities on reopen, new task, separate journal: %q", used)
	}
}

func TestPiWorkerKeepsHistoricalTaskAffinity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.jsonl")
	storage, err := session.CreateJSONLStorage(path, session.Header{ID: "superagent"})
	if err != nil {
		t.Fatal(err)
	}
	old := Trigger{Source: "s", ID: "1", Thread: "thread", Kind: "request", Text: "old"}
	entries := []transition{{State: "received", Trigger: &old}, {State: "routing"}, {State: "queued", Task: "task-1"}, {State: "running"}, {State: "completed", Text: "done", Snapshot: json.RawMessage(`{"session_id":"task-1"}`)}}
	for i, tr := range entries {
		raw, err := json.Marshal(tr)
		if err != nil {
			t.Fatal(err)
		}
		_, err = storage.AppendEntry(session.MainLane, session.NewEntry{Type: session.EntryTypeCustom, ID: strconv.Itoa(i + 1), Custom: &session.CustomData{CustomType: journalKind, Payload: raw}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	var providerID string
	def := agent.AgentDefinition{Model: agent.StreamFunc(func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
		providerID = req.SessionID
		return offlineAnswer("done"), nil
	})}
	work, err := PiWorker(def)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Open(t.Context(), path, 1, func(context.Context, Trigger, []TaskInfo) (Route, error) {
		t.Error("historical thread rerouted")
		return Route{}, nil
	}, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{Source: "s", ID: "2", Thread: "thread", Kind: "request", Text: "resume"})
	waitIdle(t, a)
	if providerID != "task-1" {
		t.Fatalf("historical provider ID changed to %q", providerID)
	}
}

func TestPiWorkerRestoresModelIdentityFromCurrentHost(t *testing.T) {
	for _, tc := range []struct {
		name, provider, model string
		sameProvider          bool
	}{
		{"saved default", "host", "default", true}, {"another host model", "host", "selected", true}, {"other provider", "other", "selected", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var request agent.ModelRequest
			var resolved, snapshotModel agent.ModelRef
			host := agent.ModelRef{Provider: "host", Model: "default", ProviderConfig: agent.ProviderConfig{BaseURL: "https://host.invalid", APIKey: "host-key", Headers: map[string]string{"Proof": "host-header"}}, Metadata: map[string]any{"runtime": "private-host-metadata"}}
			def := agent.AgentDefinition{DefaultModel: host, ModelResolver: func(_ context.Context, ref agent.ModelRef, snapshot agent.AgentSnapshot) (agent.StreamModel, error) {
				resolved, snapshotModel = ref, snapshot.Model
				return agent.StreamFunc(func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
					request = req
					return offlineAnswer("done"), nil
				}), nil
			}}
			work, err := PiWorker(def)
			if err != nil {
				t.Fatal(err)
			}
			host.ProviderConfig.Headers["Proof"] = "changed after construction"
			saved := agent.AgentSnapshot{SessionID: "T", Model: agent.ModelRef{Provider: tc.provider, Model: tc.model, ProviderConfig: agent.ProviderConfig{BaseURL: "https://stale.invalid", APIKey: "stale-key"}, Metadata: map[string]any{"stale": "stale-secret"}}}
			out, err := work(t.Context(), Job{TaskID: "T", Snapshot: saved})
			if err != nil {
				t.Fatal(err)
			}
			for _, ref := range []agent.ModelRef{resolved, snapshotModel, request.Model, out.Snapshot.Model} {
				if ref.Provider != tc.provider || ref.Model != tc.model {
					t.Fatalf("lost selected identity %q/%q; want %q/%q", ref.Provider, ref.Model, tc.provider, tc.model)
				}
			}
			for _, ref := range []agent.ModelRef{resolved, snapshotModel, request.Model} {
				if tc.sameProvider {
					if ref.ProviderConfig.APIKey != "host-key" || ref.ProviderConfig.BaseURL != "https://host.invalid" || ref.ProviderConfig.Headers["Proof"] != "host-header" {
						t.Fatal("selected host model did not use the frozen current host settings")
					}
				} else if ref.ProviderConfig.APIKey != "" || ref.ProviderConfig.BaseURL != "" || len(ref.ProviderConfig.Headers) != 0 {
					t.Fatal("credentials or endpoint crossed providers")
				}
				if ref.Metadata["stale"] != nil {
					t.Fatal("restored stale metadata into runtime")
				}
			}
			if out.Snapshot.Model.ProviderConfig.APIKey != "" || out.Snapshot.Model.ProviderConfig.BaseURL != "" || len(out.Snapshot.Model.ProviderConfig.Headers) != 0 || len(out.Snapshot.Model.Metadata) != 0 {
				t.Fatal("persisted host runtime configuration")
			}
		})
	}
}

func TestPiWorkerResumesSavedModelWithRotatedHostConfiguration(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.jsonl")
	for _, cfg := range []struct{ event, model, key string }{{"one", "original", "host-key-one"}, {"two", "new-default", "host-key-two"}} {
		var used agent.ModelRef
		worker, err := PiWorker(agent.AgentDefinition{
			DefaultModel: agent.ModelRef{Provider: "P", Model: cfg.model, ProviderConfig: agent.ProviderConfig{APIKey: cfg.key}},
			Model: agent.StreamFunc(func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
				used = req.Model
				return offlineAnswer("done"), nil
			}),
		})
		if err != nil {
			t.Fatal(err)
		}
		app, err := Open(t.Context(), file, 1, func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil }, worker)
		if err != nil {
			t.Fatal(err)
		}
		submit(t, app, Trigger{ID: cfg.event, Source: "s", Thread: "one", Kind: "request", Text: "work"})
		waitIdle(t, app)
		if err := app.Close(); err != nil {
			t.Fatal(err)
		}
		if used.Model != "original" || used.ProviderConfig.APIKey != cfg.key {
			t.Fatalf("event=%q ran wrong model or host credentials", cfg.event)
		}
	}
	journal, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(journal, []byte("host-key-")) {
		t.Fatal("journal contains host credentials")
	}
}

func TestPiWorkerUsesBuiltinResolverForSavedProvider(t *testing.T) {
	var observed agent.ModelRef
	work, err := PiWorker(agent.AgentDefinition{
		DefaultModel: agent.ModelRef{Provider: "host", Model: "default", ProviderConfig: agent.ProviderConfig{APIKey: "host-secret", Headers: map[string]string{"Auth": "host"}, BaseURL: "https://host.invalid"}},
		PrepareRequest: func(_ context.Context, req agent.PrepareRequestContext) (*agent.AgentLoopTurnUpdate, error) {
			observed = req.Model
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	const provider = "unregistered-superagent-fixture"
	const model = "missing"
	_, err = work(t.Context(), Job{TaskID: "task", Snapshot: agent.AgentSnapshot{SessionID: "task", Model: agent.ModelRef{Provider: provider, Model: model, ProviderConfig: agent.ProviderConfig{APIKey: "stale-key", BaseURL: "https://stale.invalid"}, Metadata: map[string]any{"stale": "secret"}}}})
	// The native registry rejects this private fixture name before opening any transport.
	if err == nil || !strings.Contains(err.Error(), `unsupported provider/model "unregistered-superagent-fixture"/"missing"`) {
		t.Fatalf("expected native model lookup error; got %v", err)
	}
	if observed.Provider != provider || observed.Model != model || observed.ProviderConfig.APIKey != "" || observed.ProviderConfig.BaseURL != "" || len(observed.ProviderConfig.Headers) != 0 || observed.ProviderConfig.Auth != nil || len(observed.Metadata) != 0 {
		t.Fatal("native request did not receive only the saved identity")
	}
}

func TestPiWorkerStandaloneProviderSessionOverride(t *testing.T) {
	work, err := PiWorker(agent.AgentDefinition{Model: agent.StreamFunc(func(_ context.Context, r agent.ModelRequest) (agent.AssistantStream, error) {
		return offlineAnswer(r.SessionID), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	for _, wireID := range []string{"", "opaque-host-session"} {
		out, err := work(t.Context(), Job{TaskID: "T", ProviderSessionID: wireID})
		want := wireID
		if want == "" {
			want = "T"
		}
		if err != nil || out.Summary != want || out.Snapshot.SessionID != "T" {
			t.Fatalf("report=%q local snapshot owner=%q err=%v", out.Summary, out.Snapshot.SessionID, err)
		}
	}
}
