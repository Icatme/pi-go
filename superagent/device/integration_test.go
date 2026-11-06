//go:build (linux || darwin || windows) && (amd64 || arm64)

package device_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/background"
	"github.com/Icatme/pi-go/executionreceipt"
	"github.com/Icatme/pi-go/superagent"
	"github.com/Icatme/pi-go/superagent/device"
	"github.com/Icatme/pi-go/superagent/sharedsqlite"
)

type fixtureStream struct {
	message agent.Message
	events  chan agent.AssistantEvent
}

func (s *fixtureStream) Events() <-chan agent.AssistantEvent { return s.events }
func (s *fixtureStream) Wait() (agent.Message, error)        { return s.message, nil }
func (s *fixtureStream) Close() error                        { return nil }
func fixtureReply(m agent.Message) agent.AssistantStream {
	events := make(chan agent.AssistantEvent, 1)
	events <- agent.AssistantEvent{Type: agent.AssistantEventDone, Message: m}
	close(events)
	return &fixtureStream{m, events}
}
func fixtureAnswer(text string) agent.AssistantStream {
	m := agent.NewTextMessage(agent.RoleAssistant, text)
	m.StopReason = agent.StopReasonStop
	return fixtureReply(m)
}
func fixtureText(m agent.Message) string {
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Type == agent.PartTypeText {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// This is a same-host, offline protocol integration fixture, not a production
// authentication implementation. Its synchronized current lease pointer is the
// host authority; request headers alone never establish ownership.
func TestDeviceSharedJournalNativeCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	const canary = "device-only-credential-CANARY-6417"
	binding := device.Binding{Domain: "integration", Device: "fixture-device", Workspace: "private-workspace", PolicyRevision: "v1"}
	tools := []string{"slow"}
	scope := device.Scope(binding, tools)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var nativeCalls atomic.Int32
	local, err := background.NewLocal(ctx, binding.Workspace, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{{Name: "slow", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
		nativeCalls.Add(1)
		if err := executionreceipt.RequireCapture(ctx); err != nil {
			return agent.ToolResult{}, err
		}
		close(entered)
		<-release // Native host lifetime outlives both coordinator instances.
		if !executionreceipt.ReportLocalCommit(ctx) {
			return agent.ToolResult{}, errors.New("missing receipt")
		}
		return agent.ToolResult{Content: []agent.Part{agent.NewTextPart(canary)}}, nil
	}}}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var authorityMu sync.Mutex
	var current *sharedsqlite.Journal
	db := filepath.Join(dir, "coordinator.db")
	openJournal := func(owner string) *sharedsqlite.Journal {
		j, e := sharedsqlite.Open(ctx, db, sharedsqlite.Options{Domain: binding.Domain, Owner: owner})
		if e != nil {
			t.Fatal(e)
		}
		authorityMu.Lock()
		current = j
		authorityMu.Unlock()
		return j
	}
	first := openJournal("first")
	defer first.Close()
	endpoint, err := device.Create(filepath.Join(dir, "device.jsonl"), device.Config{Binding: binding, Tools: tools, Backend: local, Authorize: func(r *http.Request) (device.Authority, error) {
		authorityMu.Lock()
		defer authorityMu.Unlock()
		if current == nil || current.Context().Err() != nil || r.Header.Get("X-Fixture-Epoch") != strconv.FormatInt(current.Epoch(), 10) {
			return device.Authority{}, errors.New("no current authenticated owner")
		}
		return device.Authority{Domain: binding.Domain, Device: binding.Device, Scope: scope, Epoch: uint64(current.Epoch())}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	server := httptest.NewServer(endpoint)
	defer server.Close()
	var offline atomic.Bool
	var starts atomic.Int32
	var wireMu sync.Mutex
	var wire bytes.Buffer
	var inspected []string
	transport := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		if offline.Load() {
			return nil, errors.New("fixture offline")
		}
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			return nil, e
		}
		r.Body.Close()
		var cmd struct{ Action, ID string }
		if e = json.Unmarshal(raw, &cmd); e != nil {
			return nil, e
		}
		if cmd.Action == "start" {
			starts.Add(1)
		}
		wireMu.Lock()
		wire.Write(raw)
		if cmd.Action == "inspect" {
			inspected = append(inspected, cmd.ID)
		}
		wireMu.Unlock()
		clone := r.Clone(r.Context())
		clone.Body = io.NopCloser(bytes.NewReader(raw))
		authorityMu.Lock()
		clone.Header.Set("X-Fixture-Epoch", strconv.FormatInt(current.Epoch(), 10))
		authorityMu.Unlock()
		response, e := server.Client().Transport.RoundTrip(clone)
		if e != nil {
			return nil, e
		}
		body, e := io.ReadAll(response.Body)
		response.Body.Close()
		if e != nil {
			return nil, e
		}
		wireMu.Lock()
		wire.Write(body)
		wireMu.Unlock()
		response.Body = io.NopCloser(bytes.NewReader(body))
		return response, nil
	})
	client, err := device.NewClient(device.ClientOptions{URL: server.URL, Transport: transport, Binding: binding, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	var follow, completions, results atomic.Int32
	var completionMu sync.Mutex
	var received superagent.BackgroundCompletion
	model := agent.StreamFunc(func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
		raw, _ := json.Marshal(req)
		if bytes.Contains(raw, []byte(canary)) {
			return nil, errors.New("credential reached model")
		}
		var last agent.Message
		for _, m := range req.Messages {
			if m.Role != agent.RoleSystem {
				last = m
			}
		}
		if last.Role == agent.RoleTool {
			if last.ToolResult.ToolCallID != "native-launch" {
				return nil, errors.New("wrong tool response")
			}
			results.Add(1)
			return fixtureAnswer("Started."), nil
		}
		var envelope struct {
			Completion *superagent.BackgroundCompletion `json:"background_completion"`
		}
		if e := json.Unmarshal([]byte(fixtureText(last)), &envelope); e != nil {
			return nil, e
		}
		if envelope.Completion != nil {
			if last.Role != agent.RoleUser {
				return nil, errors.New("completion must be new user input")
			}
			completionMu.Lock()
			received = *envelope.Completion
			completionMu.Unlock()
			completions.Add(1)
			return fixtureAnswer("Recorded completion."), nil
		}
		var trigger superagent.Trigger
		if e := json.Unmarshal([]byte(fixtureText(last)), &trigger); e != nil {
			return nil, e
		}
		if trigger.ID == "followup" {
			follow.Add(1)
			return fixtureAnswer("Handled another input."), nil
		}
		m := agent.NewTextMessage(agent.RoleAssistant, "")
		m.StopReason = agent.StopReasonToolUse
		m.ToolCalls = []agent.ToolCall{{ID: "native-launch", Name: "background_start", Arguments: json.RawMessage(`{"tool":"slow","arguments":{}}`)}}
		return fixtureReply(m), nil
	})
	piWorker, err := superagent.PiBackgroundWorker(agent.AgentDefinition{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	quotaRejected := make(chan bool, 1)
	worker := func(ctx context.Context, j superagent.Job) (superagent.Outcome, error) {
		if j.Trigger.ID == "quota-probe" {
			_, e := j.Background.Start(ctx, superagent.BackgroundStart{Tool: "slow", CallID: "quota-probe", Arguments: json.RawMessage(`{}`)})
			quotaRejected <- e != nil
			return superagent.Outcome{Summary: "Quota checked.", Snapshot: agent.AgentSnapshot{SessionID: j.TaskID}}, nil
		}
		return piWorker(ctx, j)
	}
	route := func(_ context.Context, j superagent.MainJob) (superagent.MainOutcome, error) {
		action := "ignore"
		if j.Input.Kind == "user" {
			action = "new"
		}
		return superagent.MainOutcome{Decision: superagent.MainDecision{Action: action}, Snapshot: j.Snapshot}, nil
	}
	opts := superagent.BackgroundOptions{MaxConcurrent: 1, PollInterval: time.Minute, Tools: []superagent.BackgroundTool{{Name: "slow", Scope: client.Scope(), PolicyRevision: binding.PolicyRevision, Backend: client, Authorize: func(_ context.Context, a superagent.BackgroundAuthorization) error {
		authorityMu.Lock()
		defer authorityMu.Unlock()
		if current.Context().Err() != nil || a.Scope != scope || a.Target.TaskID == "" || a.DeliveryID == "" {
			return errors.New("invalid current authority")
		}
		return nil
	}}}}
	open := func(j *sharedsqlite.Journal) *superagent.SuperAgent {
		a, e := superagent.OpenMainJournal(ctx, j, superagent.MainJournalOptions{Parallel: 1, Router: route, Worker: worker, Background: &opts})
		if e != nil {
			t.Fatal(e)
		}
		return a
	}
	idle := func(a *superagent.SuperAgent) {
		if e := a.WaitIdle(ctx); e != nil {
			t.Fatal(e)
		}
	}
	a := open(first)
	defer a.Close()
	if _, err = a.Submit(superagent.Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request", Text: "start"}); err != nil {
		t.Fatal(err)
	}
	idle(a)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	original := a.BackgroundOperations()[0]
	if !original.Started || original.Scope != scope {
		t.Fatalf("unexpected admission: %+v", original)
	}
	if _, err = a.SubmitTask(superagent.Trigger{ID: "followup", Source: "im", Thread: "chat", Kind: "request", Text: "new input"}, original.TaskID); err != nil {
		t.Fatal(err)
	}
	idle(a)
	if follow.Load() != 1 || completions.Load() != 0 || results.Load() != 1 {
		t.Fatal("same Worker did not remain available while native body was blocked")
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	offline.Store(true)
	second := openJournal("replacement")
	if second.Epoch() <= first.Epoch() {
		t.Fatal("replacement did not advance epoch")
	}
	b := open(second)
	defer b.Close()
	if err = b.RefreshBackground(ctx, original.ID); err != nil {
		t.Fatal(err)
	}
	pending := b.BackgroundOperations()[0]
	if pending.ID != original.ID || pending.Scope != scope || pending.DeliveryID != original.DeliveryID || !pending.NeedsReview || pending.Exited {
		t.Fatalf("offline recovery lost original pending identity: %+v", pending)
	}
	if err = b.ReconcileBackground(original.ID, "unverified assumption"); err == nil {
		t.Fatal("unknown operation reconciled without exit evidence")
	}
	if _, err = b.Submit(superagent.Trigger{ID: "quota-probe", Source: "im", Thread: "other", Kind: "request", Text: "independent task"}); err != nil {
		t.Fatal(err)
	}
	idle(b)
	select {
	case denied := <-quotaRejected:
		if !denied {
			t.Fatal("offline operation released quota")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	offline.Store(false)
	if err = b.RefreshBackground(ctx, original.ID); err != nil {
		t.Fatal(err)
	}
	if nativeCalls.Load() != 1 || starts.Load() != 1 {
		t.Fatal("recovery redispatched native operation")
	}
	once.Do(func() { close(release) })
	for !b.BackgroundOperations()[0].Exited {
		if err = b.RefreshBackground(ctx, original.ID); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	idle(b)
	for range 3 {
		if err = b.RefreshBackground(ctx, original.ID); err != nil {
			t.Fatal(err)
		}
	}
	idle(b)
	completionMu.Lock()
	got := received
	completionMu.Unlock()
	if completions.Load() != 1 || results.Load() != 1 || starts.Load() != 1 || nativeCalls.Load() != 1 {
		t.Fatalf("completion/response/start/body counts %d/%d/%d/%d", completions.Load(), results.Load(), starts.Load(), nativeCalls.Load())
	}
	if got.OperationID != original.ID || got.CallID != original.CallID || got.DeliveryID != original.DeliveryID || len(got.Output) != 0 || len(got.Receipts) != 1 || !got.Receipts[0].Committed || !got.Receipts[0].Completed {
		t.Fatalf("late committed completion lost provenance: %+v", got)
	}
	snapshot, ok, err := b.TaskSnapshot(original.TaskID)
	if err != nil || !ok {
		t.Fatal("missing original Worker snapshot", err)
	}
	count, completionInputs := 0, 0
	for _, m := range snapshot.Messages {
		if m.ToolResult != nil && m.ToolResult.ToolCallID == original.CallID {
			count++
		}
		if m.Role == agent.RoleUser {
			var envelope struct {
				Completion *superagent.BackgroundCompletion `json:"background_completion"`
			}
			if json.Unmarshal([]byte(fixtureText(m)), &envelope) == nil && envelope.Completion != nil && envelope.Completion.OperationID == original.ID {
				completionInputs++
			}
		}
	}
	if count != 1 || completionInputs != 1 {
		t.Fatalf("original Worker native results=%d, completion inputs=%d", count, completionInputs)
	}
	if b.BackgroundOperations()[0].Scope != scope || client.Scope() != scope {
		t.Fatal("grant scope drifted")
	}
	wireMu.Lock()
	wireBytes := append([]byte(nil), wire.Bytes()...)
	ids := append([]string(nil), inspected...)
	wireMu.Unlock()
	if len(ids) == 0 {
		t.Fatal("replacement never queried pending operation")
	}
	for _, id := range ids {
		if id != original.ID {
			t.Fatalf("queried different operation %q", id)
		}
	}
	for name, value := range map[string]any{"completion": got, "journal": second.Log(), "replies": b.Replies(), "reports": b.ReportsSince(0), "snapshot": snapshot, "operations": b.BackgroundOperations()} {
		raw, e := json.Marshal(value)
		if e != nil {
			t.Fatal(e)
		}
		if bytes.Contains(raw, []byte(canary)) {
			t.Fatalf("credential leaked to %s", name)
		}
	}
	if bytes.Contains(wireBytes, []byte(canary)) {
		t.Fatal("credential leaked over device protocol")
	}
	native, err := local.Inspect(ctx, original.ID)
	if err != nil || !bytes.Contains(native.Output, []byte(canary)) {
		t.Fatal("fixture did not actually produce private device output", err)
	}
	registry, err := os.ReadFile(filepath.Join(dir, "device.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(registry, []byte(canary)) {
		t.Fatal("credential leaked to device registry")
	}
}

// ExampleNewClient runs only an in-process native callback and a loopback HTTP
// fixture. A real host supplies authenticated transport and current authority.
func ExampleNewClient() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "device-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	if err = os.Chmod(dir, 0700); err != nil {
		panic(err)
	}
	binding := device.Binding{Domain: "example", Device: "loopback", Workspace: "scratch", PolicyRevision: "v1"}
	names := []string{"greet"}
	scope := device.Scope(binding, names)
	local, err := background.NewLocal(ctx, binding.Workspace, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{{Name: "greet", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
		return agent.ToolResult{Content: []agent.Part{agent.NewTextPart("device-local output")}}, nil
	}}}}, 1)
	if err != nil {
		panic(err)
	}
	endpoint, err := device.Create(filepath.Join(dir, "registry.jsonl"), device.Config{Binding: binding, Tools: names, Backend: local, Authorize: func(*http.Request) (device.Authority, error) {
		// Fixture only: production must authenticate the caller and verify its lease.
		return device.Authority{Domain: binding.Domain, Device: binding.Device, Scope: scope, Epoch: 1}, nil
	}})
	if err != nil {
		panic(err)
	}
	defer endpoint.Close()
	server := httptest.NewServer(endpoint)
	defer server.Close()
	client, err := device.NewClient(device.ClientOptions{URL: server.URL, Transport: server.Client().Transport, Binding: binding, Tools: names})
	if err != nil {
		panic(err)
	}
	err = client.Start(ctx, background.Request{ID: "operation-1", Scope: client.Scope(), Call: agent.ToolCall{ID: "call-1", Name: "greet", Arguments: json.RawMessage(`{}`)}})
	if err != nil {
		panic(err)
	}
	for {
		observation, e := client.Inspect(ctx, "operation-1")
		if e != nil {
			panic(e)
		}
		if observation.State == "exited" {
			fmt.Println("finished:", observation.ID)
			fmt.Println("raw output withheld:", len(observation.Output) == 0)
			break
		}
		select {
		case <-ctx.Done():
			panic(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	// Output:
	// finished: operation-1
	// raw output withheld: true
}
