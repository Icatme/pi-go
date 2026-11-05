package superagent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent"
)

func historyReadAll(t *testing.T, h *MainHistory, ref HistoryRef, size, items int) (string, []HistoryLink) {
	t.Helper()
	req := HistoryRequest{Ref: ref, MaxBytes: size, MaxItems: items}
	var data strings.Builder
	var links []HistoryLink
	for n := 0; n < 64; n++ {
		p, err := h.Read(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > size || len(p.Links) > items || !utf8.ValidString(p.Data) {
			t.Fatalf("invalid bounded page: bytes=%d links=%d", len(raw), len(p.Links))
		}
		if p.Ref != ref {
			t.Fatal("reference changed")
		}
		data.WriteString(p.Data)
		links = append(links, p.Links...)
		if p.Next == "" {
			return data.String(), links
		}
		if p.Next == req.Cursor || (p.Data == "" && len(p.Links) == 0) {
			t.Fatal("continuation did not progress")
		}
		req.Cursor = p.Next
	}
	t.Fatal("unbounded pagination")
	return "", nil
}

func historyLink(t *testing.T, links []HistoryLink, relation, id string) HistoryRef {
	t.Helper()
	for _, l := range links {
		if l.Relation == relation && (id == "" || l.Ref.ID == id) {
			return l.Ref
		}
	}
	t.Fatalf("missing %s link to %q: %+v", relation, id, links)
	return HistoryRef{}
}

func historyTestView(data string, links int) (*MainHistory, HistoryRef) {
	ref := HistoryRef{Domain: "domain-a", Kind: "report", ID: "delivery-1"}
	node := &historyNode{data: data}
	for i := 0; i < links; i++ {
		node.links = append(node.links, HistoryLink{Relation: "trigger", Ref: HistoryRef{Domain: ref.Domain, Kind: "trigger", ID: fmt.Sprint(i)}})
	}
	return &MainHistory{ctx: context.Background(), domain: ref.Domain, view: "view-a", nodes: map[historyKey]*historyNode{{ref.Kind, ref.ID}: node}}, ref
}

func TestMainHistoryUTF8EscapedReconstruction(t *testing.T) {
	original := struct {
		Text string `json:"text"`
	}{strings.Repeat("汉字🙂\"\\\n\t<&>", 900)}
	raw, _ := json.Marshal(original)
	for _, size := range []int{1024, 4096, 16384} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			h, ref := historyTestView(string(raw), 19)
			data, links := historyReadAll(t, h, ref, size, 3)
			if data != string(raw) || len(links) != 19 {
				t.Fatal("content or link stream lost")
			}
			var got struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal([]byte(data), &got); err != nil || got != original {
				t.Fatalf("reconstruction: %v", err)
			}
		})
	}
}

func TestMainHistoryRejectsInvalidAndForeignRequests(t *testing.T) {
	h, ref := historyTestView(`{"text":"汉字"}`, 2)
	cursor := func(c historyCursor) string {
		raw, _ := json.Marshal(c)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	base := historyCursor{View: h.view, Ref: ref}
	requests := []HistoryRequest{
		{Ref: ref, MaxBytes: 511, MaxItems: 1}, {Ref: ref, MaxBytes: 16385, MaxItems: 1},
		{Ref: ref, MaxBytes: 512, MaxItems: 0}, {Ref: ref, MaxBytes: 512, MaxItems: 33},
		{Ref: HistoryRef{Domain: "domain-b", Kind: ref.Kind, ID: ref.ID}, MaxBytes: 512, MaxItems: 1},
		{Ref: HistoryRef{Domain: ref.Domain, Kind: "snapshot", ID: ref.ID}, MaxBytes: 512, MaxItems: 1},
		{Ref: ref, MaxBytes: 512, MaxItems: 1, Cursor: "%%%"},
	}
	for _, change := range []func(*historyCursor){func(c *historyCursor) { c.Text = -1 }, func(c *historyCursor) { c.Text = 99999 }, func(c *historyCursor) { c.Text = 10 }, func(c *historyCursor) { c.Link = -1 }, func(c *historyCursor) { c.Link = 3 }, func(c *historyCursor) { c.View = "other" }, func(c *historyCursor) { c.Ref.Domain = "domain-b" }, func(c *historyCursor) { c.Ref.Kind = "task" }} {
		c := base
		change(&c)
		requests = append(requests, HistoryRequest{Ref: ref, MaxBytes: 512, MaxItems: 1, Cursor: cursor(c)})
	}
	for i, req := range requests {
		if _, err := h.Read(t.Context(), req); err == nil {
			t.Errorf("invalid request %d accepted", i)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := h.Read(ctx, HistoryRequest{Ref: ref, MaxBytes: 512, MaxItems: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	h.close()
	if _, err := h.Read(t.Context(), HistoryRequest{Ref: ref, MaxBytes: 512, MaxItems: 1}); err == nil {
		t.Fatal("expired view readable")
	}
	h, ref = historyTestView(`{}`, 0)
	h.ctx = ctx
	if _, err := h.Read(t.Context(), HistoryRequest{Ref: ref, MaxBytes: 512, MaxItems: 1}); err == nil {
		t.Fatal("canceled owner readable")
	}
}

func TestMainHistoryInvocationBudgetsAndMutation(t *testing.T) {
	h, ref := historyTestView(`{}`, 2)
	req := HistoryRequest{Ref: ref, MaxBytes: 1024, MaxItems: 2}
	p, err := h.Read(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	p.Links[0].Ref.Domain = "poison"
	p.Links[0].Summary = "poison"
	p, err = h.Read(t.Context(), req)
	if err != nil || p.Links[0].Ref.Domain == "poison" || p.Links[0].Summary == "poison" {
		t.Fatal("returned mutation changed view")
	}
	for i := h.calls; i < 64; i++ {
		if _, err := h.Read(t.Context(), HistoryRequest{}); err == nil {
			t.Fatal("invalid read passed")
		}
	}
	if _, err := h.Read(t.Context(), req); err == nil {
		t.Fatal("invalid attempts did not consume budget")
	}
	h, ref = historyTestView(strings.Repeat("x", 16300), 0)
	req.Ref = ref
	req.MaxBytes = 16384
	total := 0
	for i := 0; i < 64; i++ {
		p, err := h.Read(t.Context(), req)
		if err != nil {
			if total == 0 {
				t.Fatal(err)
			}
			break
		}
		raw, _ := json.Marshal(p)
		total += len(raw)
	}
	if total > 256<<10 || h.bytes != total {
		t.Fatalf("total budget breached: %d", total)
	}
	if _, err := h.Read(t.Context(), req); err == nil {
		t.Fatal("exhausted invocation accepted another full read")
	}
}

func TestMainHistoryEscapingExpansionFailsExplicitlyAtBudget(t *testing.T) {
	trigger := Trigger{ID: "large", Source: "im", Thread: "chat", Kind: "request", Text: strings.Repeat("<", 64<<10)}
	raw, err := json.Marshal(trigger)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 256<<10 {
		t.Fatal("fixture does not exceed invocation budget")
	}
	h, ref := historyTestView(string(raw), 0)
	req := HistoryRequest{Ref: ref, MaxBytes: 16384, MaxItems: 32}
	total, source := 0, 0
	for range 64 {
		p, err := h.Read(t.Context(), req)
		if err != nil {
			if !strings.Contains(err.Error(), "budget") || total == 0 || total > 256<<10 || source >= len(raw) {
				t.Fatalf("unexpected termination: total=%d source=%d err=%v", total, source, err)
			}
			return
		}
		encoded, _ := json.Marshal(p)
		total += len(encoded)
		source += len(p.Data)
		if len(encoded) > req.MaxBytes || !utf8.ValidString(p.Data) {
			t.Fatal("invalid oversized page")
		}
		if p.Next == "" {
			t.Fatal("oversized escaped content falsely reported complete")
		}
		req.Cursor = p.Next
	}
	t.Fatal("expanded content did not explicitly fail its byte budget")
}

func TestMainHistoryCursorExpiresAcrossViewsAndZeroValue(t *testing.T) {
	h, ref := historyTestView(strings.Repeat("汉", 2000), 0)
	p, err := h.Read(t.Context(), HistoryRequest{Ref: ref, MaxBytes: 512, MaxItems: 1})
	if err != nil || p.Next == "" {
		t.Fatalf("continuation: %v", err)
	}
	next, _ := historyTestView(strings.Repeat("汉", 2000), 0)
	next.view = "next-inference"
	if _, err := next.Read(t.Context(), HistoryRequest{Ref: ref, Cursor: p.Next, MaxBytes: 512, MaxItems: 1}); err == nil {
		t.Fatal("old cursor survived inference boundary")
	}
	var zero MainHistory
	if _, err := zero.Read(t.Context(), HistoryRequest{Ref: ref, MaxBytes: 512, MaxItems: 1}); err == nil {
		t.Fatal("zero-value reader accepted read")
	}
}

func TestMainHistoryFinalPageDropsUnfittableContinuation(t *testing.T) {
	for _, links := range []int{0, 1} {
		t.Run(fmt.Sprint(links), func(t *testing.T) {
			h, ref := historyTestView(`{"text":"final"}`, links)
			h.view = strings.Repeat("v", 400)
			// The complete object fits, but even an empty continued page does not.
			node := h.nodes[historyKey{ref.Kind, ref.ID}]
			full, _ := json.Marshal(HistoryPage{Ref: ref, Data: node.data, Links: node.links})
			c, _ := json.Marshal(historyCursor{View: h.view, Ref: ref})
			partial, _ := json.Marshal(HistoryPage{Ref: ref, Next: base64.RawURLEncoding.EncodeToString(c)})
			if len(full) > 512 || len(partial) <= 512 {
				t.Fatal("invalid final-envelope fixture")
			}
			p, err := h.Read(t.Context(), HistoryRequest{Ref: ref, MaxBytes: 512, MaxItems: 1})
			if err != nil || p.Next != "" || p.Data != node.data || len(p.Links) != links {
				t.Fatalf("complete 512-byte page rejected: %+v %v", p, err)
			}
		})
	}
}

// Real admissions, Worker publication, callback lifetime and replay exercise
// the public chain without treating historical IDs as new routing authority.
func TestMainHistoryOldTaskRecallReplayAndAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.jsonl")
	entered := make(chan MainJob, 1)
	release := make(chan MainDecision, 1)
	route := func(ctx context.Context, job MainJob) (MainOutcome, error) {
		d := MainDecision{Action: "new"}
		if job.Input.Kind != "user" {
			d = MainDecision{Action: "ignore"}
		}
		if strings.HasPrefix(job.Input.Trigger.ID, "recall") {
			entered <- job
			select {
			case d = <-release:
			case <-ctx.Done():
				return MainOutcome{}, ctx.Err()
			}
		}
		return MainOutcome{Decision: d, Snapshot: job.Snapshot}, nil
	}
	work := func(_ context.Context, job Job) (Outcome, error) {
		return Outcome{Summary: "public " + job.Trigger.Text, Snapshot: agent.AgentSnapshot{SessionID: job.TaskID, Messages: []agent.Message{agent.NewUserTextMessage("PRIVATE_TOOL_TRANSCRIPT_SECRET")}, Metadata: map[string]any{"credential": "PRIVATE_SNAPSHOT_SECRET"}}}, nil
	}
	a, err := OpenMain(t.Context(), path, 1, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for i := 0; i < 130; i++ {
		submit(t, a, Trigger{ID: fmt.Sprintf("seed-%d", i), Source: "im", Thread: fmt.Sprintf("chat-%d", i), Kind: "request", Text: fmt.Sprint(i)})
		waitIdle(t, a)
	}
	oldID := a.Status().Tasks[0].ID
	pr := Trigger{ID: "pr-review", Source: "github", Thread: "pr-27", Kind: "signal", Text: "exact PR result 汉字"}
	if ok, err := a.SubmitTask(pr, oldID); !ok || err != nil {
		t.Fatalf("PR admission: %v %v", ok, err)
	}
	waitIdle(t, a)
	submit(t, a, Trigger{ID: "recall-first", Source: "im", Thread: "chat-0", Kind: "request", Text: "what happened?"})
	job := receive(t, entered)
	if len(job.Tasks) != 128 {
		t.Fatalf("candidate count=%d", len(job.Tasks))
	}
	for _, task := range job.Tasks {
		if task.ID == oldID {
			t.Fatal("old task unexpectedly offered")
		}
	}
	if len(job.Input.HistoryRefs) != 2 {
		t.Fatalf("initial references: %+v", job.Input.HistoryRefs)
	}
	_, threadLinks := historyReadAll(t, job.History, job.Input.HistoryRefs[1], 4096, 4)
	oldTrigger := historyLink(t, threadLinks, "trigger", "delivery-1")
	_, triggerLinks := historyReadAll(t, job.History, oldTrigger, 4096, 4)
	oldTask := historyLink(t, triggerLinks, "task", oldID)
	_, taskLinks := historyReadAll(t, job.History, oldTask, 4096, 4)
	var prRef HistoryRef
	for _, l := range taskLinks {
		if l.Relation == "trigger" && l.Ref != oldTrigger {
			prRef = l.Ref
		}
	}
	if prRef.ID == "" {
		t.Fatal("missing related PR trigger")
	}
	_, prLinks := historyReadAll(t, job.History, prRef, 4096, 4)
	reportRef := historyLink(t, prLinks, "report", "")
	reportData, _ := historyReadAll(t, job.History, reportRef, 4096, 4)
	var report Report
	if err := json.Unmarshal([]byte(reportData), &report); err != nil {
		t.Fatal(err)
	}
	if report.Text != "public "+pr.Text || report.EventID != pr.ID || report.TaskID != oldID {
		t.Fatalf("wrong exact report: %+v", report)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []HistoryRef{job.Input.HistoryRefs[1], oldTrigger, oldTask, prRef, reportRef} {
		data, links := historyReadAll(t, job.History, ref, 16384, 32)
		raw, _ := json.Marshal(links)
		if strings.Contains(data+string(raw), "PRIVATE_") {
			t.Fatal("private Worker data leaked")
		}
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("recall wrote journal")
	}
	release <- MainDecision{Action: "attach", TaskID: oldTask.ID}
	waitIdle(t, a)
	if len(a.MainFailures()) != 1 {
		t.Fatal("recalled unoffered task acquired attach authority")
	}
	if _, err := job.History.Read(t.Context(), HistoryRequest{Ref: reportRef, MaxBytes: 4096, MaxItems: 4}); err == nil {
		t.Fatal("callback history survived return")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMain(t.Context(), path, 1, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if ok, err := b.SubmitTask(pr, oldID); ok || err != nil {
		t.Fatalf("duplicate changed on replay: %v %v", ok, err)
	}
	submit(t, b, Trigger{ID: "recall-replay", Source: "im", Thread: "chat-0", Kind: "request"})
	replay := receive(t, entered)
	replayData, _ := historyReadAll(t, replay.History, reportRef, 4096, 4)
	if replay.SessionID != job.SessionID || replayData != reportData {
		t.Fatal("durable history identity/content changed")
	}
	release <- MainDecision{Action: "ignore"}
	waitIdle(t, b)
}

func TestMainHistoryCatalogFrozenAndPrivate(t *testing.T) {
	a := &SuperAgent{ctx: t.Context(), main: mainState{id: "domain-a"}, tasks: map[string]*task{"task-1": {id: "task-1", summary: "public summary", snapshot: json.RawMessage(`{"secret":"PRIVATE_SNAPSHOT"}`)}}, events: []item{{admission: 0, task: "task-1", trigger: Trigger{ID: "one", Source: "im", Thread: "chat", Text: "public input"}, cause: errors.New("PRIVATE_ERROR")}}}
	a.mu.Lock()
	first := a.newMainHistory()
	a.mu.Unlock()
	ref := HistoryRef{Domain: "domain-a", Kind: "task", ID: "task-1"}
	original, links := historyReadAll(t, first, ref, 4096, 32)
	a.mu.Lock()
	a.events = append(a.events, item{admission: 1, task: "task-1", trigger: Trigger{ID: "two", Source: "im", Thread: "chat", Text: "later"}})
	a.reports = append(a.reports, Report{DeliveryID: "delivery-1", TaskID: "task-1", Text: "public partial", Status: "failed", NeedsReview: true})
	second := a.newMainHistory()
	a.mu.Unlock()
	frozen, frozenLinks := historyReadAll(t, first, ref, 4096, 32)
	if frozen != original || !reflect.DeepEqual(links, frozenLinks) {
		t.Fatal("frozen view changed after admission/report")
	}
	_, newLinks := historyReadAll(t, second, ref, 4096, 32)
	if len(newLinks) <= len(links) {
		t.Fatal("fresh view missing new facts")
	}
	for key := range second.nodes {
		data, links := historyReadAll(t, second, HistoryRef{Domain: "domain-a", Kind: key.kind, ID: key.id}, 4096, 32)
		raw, _ := json.Marshal(links)
		if strings.Contains(data+string(raw), "PRIVATE_") {
			t.Fatal("private state leaked")
		}
	}
}

func TestMainHistoryIndependentJournalsRejectIdenticalIDsAndCursors(t *testing.T) {
	entered := make(chan MainJob, 2)
	route := func(ctx context.Context, job MainJob) (MainOutcome, error) {
		entered <- job
		<-ctx.Done()
		return MainOutcome{}, ctx.Err()
	}
	var jobs []MainJob
	for range 2 {
		a, err := OpenMain(t.Context(), filepath.Join(t.TempDir(), "domain.jsonl"), 1, route, mainWorker)
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		submit(t, a, Trigger{ID: "identical", Source: "im", Thread: "chat", Kind: "request", Text: strings.Repeat("汉字", 3000)})
		jobs = append(jobs, receive(t, entered))
	}
	left, right := jobs[0].Input.HistoryRefs[0], jobs[1].Input.HistoryRefs[0]
	if left.ID != right.ID || left.Kind != right.Kind || left.Domain == right.Domain {
		t.Fatal("fixture does not isolate identical local IDs")
	}
	p, err := jobs[0].History.Read(t.Context(), HistoryRequest{Ref: left, MaxBytes: 1024, MaxItems: 1})
	if err != nil || p.Next == "" {
		t.Fatalf("missing continuation: %v", err)
	}
	for _, req := range []HistoryRequest{
		{Ref: left, MaxBytes: 1024, MaxItems: 1},
		{Ref: left, Cursor: p.Next, MaxBytes: 1024, MaxItems: 1},
		{Ref: right, Cursor: p.Next, MaxBytes: 1024, MaxItems: 1},
	} {
		if _, err := jobs[1].History.Read(t.Context(), req); err == nil {
			t.Fatal("foreign journal reference/cursor crossed domain")
		}
	}
}

func TestMainHistoryFanoutDistinctReportsAndFrozenLiveAdmissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fanout.jsonl")
	entered := make(chan MainJob, 1)
	release := make(chan struct{})
	route := func(ctx context.Context, job MainJob) (MainOutcome, error) {
		d := MainDecision{Action: "new"}
		if job.Input.Kind != "user" {
			d = MainDecision{Action: "ignore"}
		}
		if job.Input.Trigger.ID == "recall" {
			entered <- job
			select {
			case <-release:
			case <-ctx.Done():
				return MainOutcome{}, ctx.Err()
			}
			d = MainDecision{Action: "ignore"}
		}
		return MainOutcome{Decision: d, Snapshot: job.Snapshot}, nil
	}
	a, err := OpenMain(t.Context(), path, 2, route, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, id := range []string{"one", "two"} {
		submit(t, a, Trigger{ID: id, Source: "im", Thread: "chat", Kind: "request", Text: id})
		waitIdle(t, a)
	}
	for _, task := range a.Status().Tasks {
		if err := a.SetThreadSubscription(task.ID, "github", "pr", true); err != nil {
			t.Fatal(err)
		}
	}
	event := Trigger{ID: "shared-review", Source: "github", Thread: "pr", Kind: "signal", Text: "review"}
	if ok, err := a.SubmitThread(event); !ok || err != nil {
		t.Fatalf("fanout: %v %v", ok, err)
	}
	waitIdle(t, a)
	submit(t, a, Trigger{ID: "recall", Source: "im", Thread: "chat", Kind: "request"})
	job := receive(t, entered)
	var refs []HistoryRef
	var trigger HistoryRef
	for _, r := range a.Status().Reports {
		if r.EventID == event.ID {
			refs = append(refs, HistoryRef{Domain: job.SessionID, Kind: "report", ID: r.DeliveryID})
		}
	}
	if len(refs) != 2 || refs[0] == refs[1] {
		t.Fatalf("fanout report identities: %+v", refs)
	}
	for _, ref := range refs {
		data, links := historyReadAll(t, job.History, ref, 4096, 32)
		var report Report
		if err := json.Unmarshal([]byte(data), &report); err != nil {
			t.Fatal(err)
		}
		if report.EventID != event.ID || report.DeliveryID != ref.ID {
			t.Fatal("wrong fanout report")
		}
		got := historyLink(t, links, "trigger", "")
		if trigger.ID != "" && got != trigger {
			t.Fatal("fanout did not retain one originating trigger")
		}
		trigger = got
	}
	_, links := historyReadAll(t, job.History, trigger, 4096, 32)
	reports, tasks := 0, 0
	for _, l := range links {
		if l.Relation == "report" {
			reports++
		}
		if l.Relation == "task" {
			tasks++
		}
	}
	if reports != 2 || tasks != 2 {
		t.Fatalf("lost fanout edges: %+v", links)
	}
	thread := job.Input.HistoryRefs[1]
	data, before := historyReadAll(t, job.History, thread, 1024, 1)
	// Submit returns only after durable admission. The callback remains blocked,
	// so this is a deterministic concurrent write, not a timing assumption.
	submit(t, a, Trigger{ID: "later", Source: "im", Thread: "chat", Kind: "request"})
	a.mu.Lock()
	fresh := a.newMainHistory()
	a.mu.Unlock()
	frozen, after := historyReadAll(t, job.History, thread, 1024, 1)
	if data != frozen || !reflect.DeepEqual(before, after) {
		t.Fatal("live admission changed a callback's frozen pages")
	}
	_, latest := historyReadAll(t, fresh, thread, 1024, 1)
	if len(latest) != len(before)+1 {
		t.Fatal("fresh view failed to include admitted trigger")
	}
	close(release)
	waitIdle(t, a)
}

func TestMainHistoryBatchCallbackPublicRefsAndLifetime(t *testing.T) {
	views := make(chan *MainHistory, 4)
	reportRefs := make(chan HistoryRef, 1)
	route := func(ctx context.Context, job MainBatchJob) (MainBatchOutcome, error) {
		views <- job.History
		out := MainBatchOutcome{Snapshot: job.Snapshot}
		for _, input := range job.Inputs {
			d := MainDecision{Action: "new"}
			want := 2
			if input.Kind == "worker_result" {
				want = 3
				d = MainDecision{Action: "ignore"}
			}
			if len(input.HistoryRefs) != want {
				t.Errorf("%s refs: %+v", input.Kind, input.HistoryRefs)
			}
			for _, ref := range input.HistoryRefs {
				p, err := job.History.Read(ctx, HistoryRequest{Ref: ref, MaxBytes: 4096, MaxItems: 32})
				if err != nil || p.Ref != ref {
					t.Errorf("batch callback reference unavailable: %v", err)
				}
				if ref.Kind == "report" {
					var report Report
					if err := json.Unmarshal([]byte(p.Data), &report); err != nil || input.Report == nil || !reflect.DeepEqual(report, *input.Report) {
						t.Errorf("batch report mismatch: %+v %v", report, err)
					}
					reportRefs <- ref
				}
			}
			out.Decisions = append(out.Decisions, MainInputDecision{InputID: input.ID, Decision: d})
		}
		return out, nil
	}
	a, err := OpenMainBatch(t.Context(), filepath.Join(t.TempDir(), "batch.jsonl"), 1, MainBatchOptions{MaxItems: 4, MaxInputBytes: 1 << 20}, route, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "batch", Source: "im", Thread: "chat", Kind: "request", Text: "run"})
	waitIdle(t, a)
	ref := receive(t, reportRefs)
	for range 2 {
		h := receive(t, views)
		if _, err := h.Read(t.Context(), HistoryRequest{Ref: ref, MaxBytes: 4096, MaxItems: 32}); err == nil {
			t.Fatal("batch callback reader remained active after return")
		}
	}
}
