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
	"sync"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/superagent"
)

var compareModes = []string{"superagent_subscribed", "superagent_router_only", "single_pi_async_neutral_v1", "single_pi_async_delegate_first_v1"}

const CompareScriptCallLimit = 512
const CompareWorkerSlots = 4

type CompareAck struct {
	InputID    string                 `json:"input_id"`
	DeliveryID string                 `json:"delivery_id"`
	Text       string                 `json:"text"`
	Receipt    superagent.ToolReceipt `json:"native_subtask_receipt"`
}
type CompareResult struct {
	Mode              string                 `json:"mode"`
	Repetition        int                    `json:"repetition"`
	Deliveries        []LoadDelivery         `json:"deliveries"`
	Reports           []superagent.Report    `json:"terminal_reports"`
	Replies           []superagent.Reply     `json:"undelivered_final_outputs"`
	Inputs            []superagent.MainInput `json:"main_inputs"`
	Acknowledgments   []CompareAck           `json:"admission_acknowledgments"`
	TaskWork          map[string]string      `json:"task_work"`
	Histories         map[string][]string    `json:"task_input_histories"`
	Effects           []Effect               `json:"record_effects"`
	PeakWorkers       int                    `json:"observed_peak_workers"`
	PeakTools         int                    `json:"observed_peak_tool_bodies"`
	ConflictChecked   bool                   `json:"conflicting_duplicate_checked"`
	ReopenChecked     bool                   `json:"completed_journal_reopen_checked"`
	MainScriptCalls   int                    `json:"main_script_calls"`
	WorkerScriptCalls int                    `json:"worker_script_calls"`
	Metrics           Metrics                `json:"metrics"`
}
type CompareSuite struct {
	Version         string          `json:"version"`
	Evidence        string          `json:"evidence"`
	Limitations     []string        `json:"limitations"`
	FixtureDigest   string          `json:"fixture_sha256"`
	Events          []Event         `json:"events"`
	WorkerSlots     int             `json:"worker_slots"`
	ContextBytes    int             `json:"context_bytes_per_script_request"`
	ScriptCallLimit int             `json:"script_calls_per_run_limit"`
	Runs            []CompareResult `json:"runs"`
}

func RunCompare(ctx context.Context, dir string, repeats int) (CompareSuite, error) {
	if ctx == nil || repeats < 1 || repeats > 5 {
		return CompareSuite{}, errors.New("comparison requires context and 1–5 repetitions")
	}
	events := LoadFixture()
	raw, _ := json.Marshal(events)
	digest := sha256.Sum256(raw)
	suite := CompareSuite{Version: "m9e-async-four-strategy-v1", Evidence: "actual Pi runtime entrypoints; deterministic offline scripts; conservation only", Events: events, FixtureDigest: hex.EncodeToString(digest[:]), WorkerSlots: CompareWorkerSlots, ContextBytes: ContextBytes, ScriptCallLimit: CompareScriptCallLimit, Limitations: []string{
		"Exact M9c three-phase fixture, not M9a mixed arrivals/cancellation/service-time workload or final M9 acceptance.",
		"Single-Pi asynchronous host queue v1 is a new baseline, not the synchronous M9b baseline. It retains snapshots/deduplication in memory; SuperAgent journals are durable.",
		"Each phase offers an identical prebuilt trigger sequence, not simultaneous completed admissions; phase drains establish task summaries. SuperAgent may execute during Submit calls; single-Pi processes main admission turns before later accepts and consumes queued results after all phase admissions. Tools complete independently with no release waves or imposed service durations.",
		"Neutral/delegate-first scripts make identical decisions; only prompts differ. No prompt efficacy, semantic routing or performance comparison.",
		"SuperAgent pending outbox entries and single-Pi returned final outputs are undelivered evidence. Admission acknowledgments are not effective replies.",
		"No live models, tokens, cost, latency, throughput, historical baseline or crash recovery measurement. Repetitions check conservation only."}}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return suite, err
	}
	for rep := 1; rep <= repeats; rep++ {
		for offset := range compareModes {
			mode := compareModes[(offset+rep-1)%len(compareModes)]
			path, err := os.MkdirTemp(dir, "runtimecompare-")
			if err != nil {
				return suite, err
			}
			r, err := runCompareArm(ctx, path, mode, rep, events, compareOptions{})
			removeErr := os.RemoveAll(path)
			if err != nil {
				return suite, fmt.Errorf("%s repetition %d: %w", mode, rep, err)
			}
			if removeErr != nil {
				return suite, removeErr
			}
			if err = ValidateCompare(events, r); err != nil {
				return suite, fmt.Errorf("%s: %w", mode, err)
			}
			suite.Runs = append(suite.Runs, r)
		}
	}
	return suite, nil
}

type compareOptions struct {
	beforeTool func(context.Context, Effect) error
	admitted   func(int)
}
type compareHost struct {
	h                                      *host
	mu                                     sync.Mutex
	workers, tools, peakWorkers, peakTools int
}

func newCompareHost(options compareOptions) *compareHost {
	c := &compareHost{}
	c.h = &host{script: newScript(ContextBytes, CompareScriptCallLimit)}
	c.h.beforeEffect = options.beforeTool
	c.h.toolLifecycle = func() func() {
		c.mu.Lock()
		c.tools++
		c.peakTools = max(c.peakTools, c.tools)
		c.mu.Unlock()
		return func() { c.mu.Lock(); c.tools--; c.mu.Unlock() }
	}
	return c
}
func (c *compareHost) worker() (superagent.Worker, error) {
	w, err := c.h.worker()
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, j superagent.Job) (superagent.Outcome, error) {
		c.mu.Lock()
		c.workers++
		c.peakWorkers = max(c.peakWorkers, c.workers)
		c.mu.Unlock()
		defer func() { c.mu.Lock(); c.workers--; c.mu.Unlock() }()
		return w(ctx, j)
	}, nil
}
func runCompareArm(ctx context.Context, dir, mode string, rep int, events []Event, options compareOptions) (CompareResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r := CompareResult{Mode: mode, Repetition: rep, TaskWork: map[string]string{}, Histories: map[string][]string{}}
	c := newCompareHost(options)
	var err error
	switch mode {
	case compareModes[0], compareModes[1]:
		err = c.super(ctx, dir, events, &r)
	case compareModes[2], compareModes[3]:
		err = c.single(ctx, events, &r, options)
	default:
		err = errors.New("unknown comparison mode")
	}
	if err != nil {
		return r, err
	}
	r.MainScriptCalls = c.h.script.roleCalls["router"] + c.h.script.roleCalls["single_async"]
	r.WorkerScriptCalls = c.h.script.roleCalls["worker"]
	r.Effects = c.h.effects
	r.PeakWorkers = c.peakWorkers
	r.PeakTools = c.peakTools
	r.Metrics = Metrics{UniqueEvents: len(r.Reports), Duplicates: len(r.Deliveries) - len(r.Reports), TaskCount: len(r.TaskWork), WorkerInvocations: c.h.workerInvocations, WorkerToolCalls: c.h.workerToolCalls, SubtaskToolCalls: c.h.subtaskToolCalls, RecordEffects: len(r.Effects), ToolCalls: c.h.workerToolCalls + c.h.subtaskToolCalls, Replies: len(r.Replies), ScriptCalls: c.h.script.calls()}
	return r, nil
}

// Execution receives only trigger fields. ExpectedWork is never read here.
func phaseTriggers(events []Event, phase int) []superagent.Trigger {
	out := make([]superagent.Trigger, 100)
	for i := range out {
		out[i] = events[phase*100+i].Trigger
	}
	return out
}
func (c *compareHost) super(ctx context.Context, dir string, events []Event, r *CompareResult) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	worker, err := c.worker()
	if err != nil {
		return err
	}
	router, err := superagent.PiMainRouter(agent.AgentDefinition{Model: c.h.script.model("router")}, ContextBytes)
	if err != nil {
		return err
	}
	open := func() (*superagent.SuperAgent, error) {
		return superagent.OpenMain(ctx, filepath.Join(dir, "journal.jsonl"), CompareWorkerSlots, func(ctx context.Context, j superagent.MainJob) (superagent.MainOutcome, error) {
			r.Inputs = append(r.Inputs, j.Input)
			return router(ctx, j)
		}, worker)
	}
	a, err := open()
	if err != nil {
		return err
	}
	defer func() {
		cancel()
		if a != nil {
			_ = a.Close()
		}
	}()
	submit := func(t superagent.Trigger) (bool, error) {
		if r.Mode == compareModes[0] && t.Source == "github" {
			return a.SubmitThread(t)
		}
		return a.Submit(t)
	}
	for phase := 0; phase < 3; phase++ {
		queue := phaseTriggers(events, phase)
		for _, t := range queue {
			ok, e := submit(t)
			if e != nil {
				return e
			}
			r.Deliveries = append(r.Deliveries, LoadDelivery{t.Source, t.ID, ok})
		}
		if err = a.WaitIdle(ctx); err != nil {
			return err
		}
		if len(a.MainFailures()) != 0 {
			return errors.New("comparison main failed")
		}
		if phase == 0 {
			for _, t := range a.Status().Tasks {
				var effect Effect
				if err = json.Unmarshal([]byte(t.Summary), &effect); err != nil {
					return err
				}
				r.TaskWork[t.ID] = effect.WorkID
				if r.Mode == compareModes[0] {
					if err = a.SetThreadSubscription(t.ID, "github", "pr-"+effect.WorkID, true); err != nil {
						return err
					}
				}
			}
		}
	}
	r.Reports = a.Status().Reports
	r.Replies = a.Replies()
	for task, work := range r.TaskWork {
		snap, ok, e := a.TaskSnapshot(task)
		if e != nil {
			return e
		}
		if !ok {
			return errors.New("missing snapshot")
		}
		r.Histories[work], err = history(snap)
		if err != nil {
			return err
		}
	}
	conflict := events[0].Trigger
	conflict.Text += " conflicting duplicate"
	if ok, e := submit(conflict); ok || e == nil {
		return errors.New("conflicting duplicate accepted")
	}
	r.ConflictChecked = true
	before, replies, calls := a.Status(), a.Replies(), c.h.script.calls()
	if err = a.Close(); err != nil {
		return err
	}
	a, err = open()
	if err != nil {
		return err
	}
	for _, e := range events {
		ok, e := submit(e.Trigger)
		if e != nil {
			return e
		}
		if ok {
			return errors.New("reopen accepted duplicate")
		}
	}
	if err = a.WaitIdle(ctx); err != nil {
		return err
	}
	if !reflect.DeepEqual(before, a.Status()) || !reflect.DeepEqual(replies, a.Replies()) || calls != c.h.script.calls() {
		return errors.New("replay changed state")
	}
	r.ReopenChecked = true
	return nil
}
