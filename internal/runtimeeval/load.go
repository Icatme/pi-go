package runtimeeval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/superagent"
)

const LoadBatchBytes = 32 << 10
const LoadScriptCallLimit = 512
const loadSlots = 4
const loadServiceMS = 10

var loadModes = []string{"superagent_unbatched_subscribed", "superagent_batch_subscribed"}

type LoadDelivery struct {
	Source   string `json:"source"`
	EventID  string `json:"event_id"`
	Accepted bool   `json:"accepted"`
}
type LoadBatch struct {
	Phase      int                    `json:"phase"`
	InputBytes int                    `json:"serialized_input_bytes"`
	Inputs     []superagent.MainInput `json:"inputs"`
}
type LoadToolRelease struct {
	Effect    Effect `json:"effect"`
	StartMS   int    `json:"virtual_gate_start_ms"`
	ReleaseMS int    `json:"virtual_gate_release_ms"`
}
type LoadResult struct {
	Mode             string              `json:"mode"`
	Repetition       int                 `json:"repetition"`
	Deliveries       []LoadDelivery      `json:"deliveries"`
	Reports          []superagent.Report `json:"terminal_reports"`
	Replies          []superagent.Reply  `json:"pending_replies"`
	TaskWork         map[string]string   `json:"task_work"`
	Histories        map[string][]string `json:"task_input_histories"`
	Effects          []Effect            `json:"record_effects"`
	Batches          []LoadBatch         `json:"main_invocations"`
	ToolReleases     []LoadToolRelease   `json:"virtual_tool_releases"`
	PeakTools        int                 `json:"peak_concurrently_gated_tool_bodies"`
	VirtualToolEndMS int                 `json:"virtual_tool_clock_end_ms"`
	ReopenChecked    bool                `json:"completed_journal_reopen_checked"`
	ConflictChecked  bool                `json:"conflicting_duplicate_checked"`
	Metrics          Metrics             `json:"metrics"`
}
type LoadSuite struct {
	Version            string       `json:"version"`
	Evidence           string       `json:"evidence"`
	Clock              string       `json:"clock"`
	Limitations        []string     `json:"limitations"`
	FixtureDigest      string       `json:"fixture_sha256"`
	ContextBytes       int          `json:"context_bytes_per_script_request"`
	ScriptCallLimit    int          `json:"script_calls_per_run_limit"`
	WorkerSlots        int          `json:"worker_slots"`
	IngressGoroutines  int          `json:"ingress_goroutines_per_burst"`
	MaxBatchItems      int          `json:"max_batch_items"`
	MaxBatchInputBytes int          `json:"max_batch_input_bytes"`
	Events             []Event      `json:"events"`
	Runs               []LoadResult `json:"runs"`
}

// LoadFixture has three 100-delivery phases. The phase boundary lets both arms
// learn task identity from actual completed work before same-chat corrections.
// This is a new fixture, not a substitution for M9a's cancellation workload.
func LoadFixture() []Event {
	var events []Event
	for phase := 0; phase < 3; phase++ {
		var work []Event
		for i := 0; i < 20; i++ {
			id := fmt.Sprintf("work-%02d", i)
			p, _ := json.Marshal(Payload{id, fmt.Sprintf("phase-%d-%s", phase, id)})
			tr := superagent.Trigger{ID: id, Source: "im", Thread: fmt.Sprintf("chat-%d", i/5), Kind: "request", Text: string(p)}
			if phase == 1 {
				tr.ID = "update-" + id
				tr.Kind = "signal"
			}
			if phase == 2 {
				tr.Source = "github"
				tr.Thread = "pr-" + id
				tr.Kind = "signal"
			} // Reuses IDs across sources.
			work = append(work, Event{Trigger: tr, ExpectedWork: id})
		}
		events = append(events, work...)
		noise := 80
		if phase == 2 {
			events = append(events, work...)
			noise = 60
		}
		for i := 0; i < noise; i++ {
			p, _ := json.Marshal(Payload{Value: "host-filtered noise"})
			events = append(events, Event{Trigger: superagent.Trigger{ID: fmt.Sprintf("noise-%d-%02d", phase, i), Source: "im", Thread: fmt.Sprintf("chat-%d", i%4), Kind: "noise", Text: string(p)}})
		}
	}
	return events
}

func RunLoad(ctx context.Context, dir string, repeats int) (LoadSuite, error) {
	if ctx == nil || repeats < 1 || repeats > 5 {
		return LoadSuite{}, errors.New("runtime load: context and 1–5 repetitions required")
	}
	events := LoadFixture()
	raw, _ := json.Marshal(events)
	digest := sha256.Sum256(raw)
	suite := LoadSuite{Version: "m9c-concurrent-runtime-v1", Evidence: "actual Pi/SuperAgent APIs with in-process scripted models; concurrent ingress and gated tool bodies; not live-model performance", Clock: "tool gate starts at virtual 0 ms; five four-body waves per phase, exactly 10 virtual ms each; no sleeps simulate service; runtime journal/message clocks and deadlock timeouts remain real and are not latency metrics", FixtureDigest: hex.EncodeToString(digest[:]), ContextBytes: ContextBytes, ScriptCallLimit: LoadScriptCallLimit, WorkerSlots: loadSlots, IngressGoroutines: 16, MaxBatchItems: 8, MaxBatchInputBytes: LoadBatchBytes, Events: events,
		Limitations: []string{"Two subscribed SuperAgent arms isolate unbatched versus batched routing; not the four-strategy M9 experiment or a prompt-policy comparison.", "Phase barriers and virtual tool service are fixture controls, not measured latency, throughput, or a simulation of real inference.", "Concurrent admission order, runtime IDs, report order, batch grouping and script-call totals may vary; repeated normalized conservation must not.", "No per-task cancellation, fanout, interrupted-run recovery, asynchronous single-Pi baseline, historical pre-M1 data, live provider tokens/cost, or release acceptance."}}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return LoadSuite{}, err
	}
	for _, mode := range loadModes {
		for rep := 1; rep <= repeats; rep++ {
			path, err := os.MkdirTemp(dir, "runtimeload-")
			if err != nil {
				return suite, err
			}
			result, runErr := runLoadArm(ctx, path, mode, rep, loadOptions{})
			removeErr := os.RemoveAll(path)
			if runErr != nil {
				return suite, fmt.Errorf("%s repetition %d: %w", mode, rep, runErr)
			}
			if removeErr != nil {
				return suite, removeErr
			}
			if err := ValidateLoad(events, result); err != nil {
				return suite, fmt.Errorf("%s: %w", mode, err)
			}
			suite.Runs = append(suite.Runs, result)
		}
	}
	return suite, nil
}

type loadTicket struct {
	effect Effect
	start  int
	permit chan struct{}
}
type loadToolGate struct {
	holdRelease       <-chan struct{}
	ready             chan struct{}
	readyOnce         sync.Once
	mu                sync.Mutex
	entered           chan *loadTicket
	active, peak, now int
	releases          []LoadToolRelease
}

func newLoadToolGate() *loadToolGate {
	return &loadToolGate{entered: make(chan *loadTicket, loadSlots)}
}
func (g *loadToolGate) beforeEffect(ctx context.Context, e Effect) error {
	g.mu.Lock()
	ticket := &loadTicket{effect: e, start: g.now, permit: make(chan struct{})}
	g.active++
	g.peak = max(g.peak, g.active)
	if g.active == loadSlots && g.ready != nil {
		g.readyOnce.Do(func() { close(g.ready) })
	}
	g.mu.Unlock()
	defer func() { g.mu.Lock(); g.active--; g.mu.Unlock() }()
	select {
	case g.entered <- ticket:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-ticket.permit:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (g *loadToolGate) releaseWave(ctx context.Context) error {
	tickets := make([]*loadTicket, loadSlots)
	for i := range tickets {
		select {
		case tickets[i] = <-g.entered:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if g.holdRelease != nil {
		select {
		case <-g.holdRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	g.now += loadServiceMS
	for _, ticket := range tickets {
		g.releases = append(g.releases, LoadToolRelease{ticket.effect, ticket.start, g.now})
	}
	g.mu.Unlock()
	for _, ticket := range tickets {
		close(ticket.permit)
	}
	return nil
}

type loadRouteDone struct {
	count int
	err   error
}
type loadPhase struct {
	index            int
	first            atomic.Bool
	entered, release chan struct{}
	done             chan loadRouteDone
}
type loadRouter struct {
	mu      sync.Mutex
	phase   *loadPhase
	batches []LoadBatch
}

func (r *loadRouter) nextPhase(index int) *loadPhase {
	p := &loadPhase{index: index, entered: make(chan struct{}), release: make(chan struct{}), done: make(chan loadRouteDone, 128)}
	r.mu.Lock()
	r.phase = p
	r.mu.Unlock()
	return p
}
func (r *loadRouter) before(ctx context.Context, inputs []superagent.MainInput) (*loadPhase, error) {
	raw, err := json.Marshal(inputs)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	p := r.phase
	r.batches = append(r.batches, LoadBatch{p.index, len(raw), slices.Clone(inputs)})
	r.mu.Unlock()
	if p.first.CompareAndSwap(false, true) {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
			return p, ctx.Err()
		}
	}
	return p, nil
}

type loadOptions struct {
	malformedBatch bool
	gate           *loadToolGate
}

func runLoadArm(ctx context.Context, dir, mode string, rep int, options loadOptions) (LoadResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	result := LoadResult{Mode: mode, Repetition: rep, TaskWork: map[string]string{}, Histories: map[string][]string{}}
	gate := options.gate
	if gate == nil {
		gate = newLoadToolGate()
	}
	h := &host{script: newScript(ContextBytes, LoadScriptCallLimit), beforeEffect: gate.beforeEffect}
	work, err := h.worker()
	if err != nil {
		return result, err
	}
	router := &loadRouter{}
	def := agent.AgentDefinition{Model: h.script.model("router")}
	route, err := superagent.PiMainRouter(def, ContextBytes)
	if err != nil {
		return result, err
	}
	model := h.script.model("router_batch")
	if options.malformedBatch {
		base := model
		var calls atomic.Int32
		model = agent.StreamFunc(func(ctx context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
			stream, e := base.Stream(ctx, req)
			if e != nil {
				return nil, e
			}
			if calls.Add(1) == 2 {
				_ = stream.Close()
				message := agent.NewTextMessage(agent.RoleAssistant, `{"decisions":[]}`)
				message.StopReason = agent.StopReasonStop
				return streamReply(message), nil
			}
			return stream, nil
		})
	}
	batch, err := superagent.PiMainBatchRouter(agent.AgentDefinition{Model: model}, ContextBytes)
	if err != nil {
		return result, err
	}
	path := filepath.Join(dir, "journal.jsonl")
	open := func() (*superagent.SuperAgent, error) {
		if mode == loadModes[0] {
			return superagent.OpenMain(ctx, path, loadSlots, func(ctx context.Context, j superagent.MainJob) (superagent.MainOutcome, error) {
				p, e := router.before(ctx, []superagent.MainInput{j.Input})
				if e != nil {
					return superagent.MainOutcome{}, e
				}
				out, e := route(ctx, j)
				p.done <- loadRouteDone{1, e}
				return out, e
			}, work)
		}
		if mode != loadModes[1] {
			return nil, errors.New("unknown runtime load mode")
		}
		return superagent.OpenMainBatch(ctx, path, loadSlots, superagent.MainBatchOptions{MaxItems: 8, MaxInputBytes: LoadBatchBytes}, func(ctx context.Context, j superagent.MainBatchJob) (superagent.MainBatchOutcome, error) {
			p, e := router.before(ctx, j.Inputs)
			if e != nil {
				return superagent.MainBatchOutcome{}, e
			}
			out, e := batch(ctx, j)
			p.done <- loadRouteDone{len(j.Inputs), e}
			return out, e
		}, work)
	}
	a, err := open()
	if err != nil {
		return result, err
	}
	// Cancel gated callbacks before joining Close, including every failure path.
	defer func() {
		cancel()
		if a != nil {
			_ = a.Close()
		}
	}()
	events := LoadFixture()
	result.Deliveries = make([]LoadDelivery, len(events))
	submit := func(i int) error {
		e := events[i]
		var accepted bool
		var err error
		if e.Trigger.Source == "github" {
			accepted, err = a.SubmitThread(e.Trigger)
		} else {
			accepted, err = a.Submit(e.Trigger)
		}
		result.Deliveries[i] = LoadDelivery{e.Trigger.Source, e.Trigger.ID, accepted}
		return err
	}
	for phase := 0; phase < 3; phase++ {
		p := router.nextPhase(phase)
		start := phase * 100
		if err = submit(start); err != nil {
			return result, err
		}
		select {
		case <-p.entered:
		case <-ctx.Done():
			return result, ctx.Err()
		}
		// Sixteen goroutines share a start barrier; the router is held until all
		// admissions finish. No WaitIdle occurs between individual deliveries.
		begin := make(chan struct{})
		errs := make(chan error, 16)
		var wg sync.WaitGroup
		for lane := 0; lane < 16; lane++ {
			wg.Add(1)
			go func(lane int) {
				defer wg.Done()
				<-begin
				for i := start + 1 + lane; i < start+100; i += 16 {
					if e := submit(i); e != nil {
						errs <- e
						return
					}
				}
			}(lane)
		}
		close(begin)
		wg.Wait()
		close(errs)
		for e := range errs {
			return result, e
		}
		close(p.release)
		// All twenty initial main inputs must finish script evaluation before
		// any tool effect is allowed. Workers can already overlap at the gate.
		observed := 0
		for observed < 20 {
			select {
			case done := <-p.done:
				if done.err != nil {
					return result, done.err
				}
				observed += done.count
			case <-ctx.Done():
				return result, ctx.Err()
			}
		}
		h.mu.Lock()
		effects := len(h.effects)
		h.mu.Unlock()
		if effects != phase*20 {
			return result, errors.New("effect crossed a closed tool gate")
		}
		for wave := 0; wave < 5; wave++ {
			if err = gate.releaseWave(ctx); err != nil {
				return result, err
			}
		}
		if err = a.WaitIdle(ctx); err != nil {
			return result, err
		}
		if len(a.MainFailures()) != 0 {
			return result, errors.New("load main callback failed")
		}
		if phase == 0 {
			for _, task := range a.Status().Tasks {
				var effect Effect
				if err = json.Unmarshal([]byte(task.Summary), &effect); err != nil {
					return result, err
				}
				result.TaskWork[task.ID] = effect.WorkID
				if err = a.SetThreadSubscription(task.ID, "github", "pr-"+effect.WorkID, true); err != nil {
					return result, err
				}
			}
		}
	}
	result.Reports, result.Replies = a.Status().Reports, a.Replies()
	for task, workID := range result.TaskWork {
		snap, ok, e := a.TaskSnapshot(task)
		if e != nil {
			return result, e
		}
		if !ok {
			return result, errors.New("missing private snapshot")
		}
		result.Histories[workID], err = history(snap)
		if err != nil {
			return result, err
		}
	}
	result.Effects = slices.Clone(h.effects)
	slices.SortFunc(result.Effects, func(a, b Effect) int {
		if a.Source+"/"+a.EventID < b.Source+"/"+b.EventID {
			return -1
		}
		if a == b {
			return 0
		}
		return 1
	})
	result.Batches = router.batches
	result.ToolReleases, result.PeakTools, result.VirtualToolEndMS = gate.releases, gate.peak, gate.now
	result.Metrics = Metrics{UniqueEvents: len(result.Reports), Duplicates: len(events) - len(result.Reports), TaskCount: len(result.TaskWork), WorkerInvocations: h.workerInvocations, WorkerToolCalls: h.workerToolCalls, RecordEffects: len(h.effects), ToolCalls: h.workerToolCalls, Replies: len(result.Replies), ScriptCalls: h.script.calls()}
	conflict := events[0].Trigger
	conflict.Text += " conflicting duplicate"
	if accepted, e := a.Submit(conflict); accepted || e == nil {
		return result, errors.New("conflicting duplicate accepted")
	}
	result.ConflictChecked = true
	before, replies, calls := a.Status(), a.Replies(), h.script.calls()
	if err = a.Close(); err != nil {
		return result, err
	}
	a, err = open()
	if err != nil {
		return result, err
	}
	for _, e := range events {
		var accepted bool
		if e.Trigger.Source == "github" {
			accepted, err = a.SubmitThread(e.Trigger)
		} else {
			accepted, err = a.Submit(e.Trigger)
		}
		if err != nil {
			return result, err
		}
		if accepted {
			return result, errors.New("reopen accepted duplicate")
		}
	}
	if err = a.WaitIdle(ctx); err != nil {
		return result, err
	}
	if !reflect.DeepEqual(before, a.Status()) || !reflect.DeepEqual(replies, a.Replies()) || h.script.calls() != calls {
		return result, errors.New("reopen changed state or repeated execution")
	}
	result.ReopenChecked = true
	return result, nil
}
