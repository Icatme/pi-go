// Package runtimeeval exercises real Pi/SuperAgent entrypoints with an offline
// script. It is a serial protocol/conservation probe, not a performance benchmark.
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

const ContextBytes = 1 << 20
const ScriptCallLimit = 128

var modes = []string{"superagent_subscribed", "superagent_router_only", "single_pi_neutral", "single_pi_delegate_first"}

type Payload struct {
	WorkID string `json:"work_id"`
	Value  string `json:"value"`
}
type Event struct {
	Trigger      superagent.Trigger `json:"trigger"`
	ExpectedWork string             `json:"expected_work,omitempty"`
}
type Effect struct {
	Source  string `json:"source"`
	EventID string `json:"event_id"`
	WorkID  string `json:"work_id"`
	Value   string `json:"value"`
}
type Delivery struct {
	Source    string `json:"source"`
	EventID   string `json:"event_id"`
	Duplicate bool   `json:"duplicate"`
	WorkID    string `json:"work_id,omitempty"`
	Status    string `json:"status"`
	Receipts  int    `json:"completed_tool_receipts"`
}
type Metrics struct {
	UniqueEvents      int      `json:"unique_events"`
	Duplicates        int      `json:"duplicates"`
	TaskCount         int      `json:"task_count"`
	WorkerInvocations int      `json:"worker_invocations"`
	WorkerToolCalls   int      `json:"worker_tool_calls"`
	SubtaskToolCalls  int      `json:"subtask_tool_calls"`
	RecordEffects     int      `json:"record_effects"`
	ToolCalls         int      `json:"total_tool_calls"`
	Replies           int      `json:"replies"`
	ScriptCalls       int      `json:"script_calls"`
	LiveModelCalls    int      `json:"live_model_calls"`
	InputTokens       *int64   `json:"input_tokens"`
	OutputTokens      *int64   `json:"output_tokens"`
	CostUSD           *float64 `json:"cost_usd"`
	LatencyMS         *float64 `json:"latency_ms"`
}
type Result struct {
	Mode          string              `json:"mode"`
	Repetition    int                 `json:"repetition"`
	Deliveries    []Delivery          `json:"deliveries"`
	Effects       []Effect            `json:"effects"`
	Replies       []Effect            `json:"replies"`
	Histories     map[string][]string `json:"task_input_histories"`
	ReopenChecked bool                `json:"superagent_reopen_checked"`
	Metrics       Metrics             `json:"metrics"`
	MainInputs    map[string]int      `json:"main_input_kinds"`
}
type Suite struct {
	Version         string   `json:"version"`
	Evidence        string   `json:"evidence"`
	Limitations     []string `json:"limitations"`
	ContextBytes    int      `json:"context_bytes_per_script_request"`
	ScriptCallLimit int      `json:"script_calls_per_run_limit"`
	WorkerSlots     int      `json:"worker_slots"`
	FixtureDigest   string   `json:"fixture_sha256"`
	Events          []Event  `json:"events"`
	Runs            []Result `json:"runs"`
}

// Fixture is intentionally smaller than M9a. Both independent requests share
// one chat. Follow-ups share that chat; callback threads are task-specific.
func Fixture() []Event {
	makeEvent := func(id, source, thread, kind, work, value string) Event {
		b, _ := json.Marshal(Payload{work, value})
		return Event{superagent.Trigger{ID: id, Source: source, Thread: thread, Kind: kind, Text: string(b)}, work}
	}
	events := []Event{
		makeEvent("a", "im", "chat", "request", "A", "initial-A"),
		makeEvent("b", "im", "chat", "request", "B", "initial-B"),
		makeEvent("n1", "im", "chat", "noise", "", "noise"),
		makeEvent("a-update", "im", "chat", "signal", "A", "corrected-A"),
		makeEvent("b-update", "im", "chat", "signal", "B", "corrected-B"),
		makeEvent("n2", "im", "chat", "noise", "", "noise"),
		makeEvent("a", "github", "pr-A", "signal", "A", "tested-A"),
		makeEvent("callback-B", "github", "pr-B", "signal", "B", "tested-B"),
		makeEvent("n3", "im", "chat", "noise", "", "noise"),
		makeEvent("n4", "im", "chat", "noise", "", "noise"),
	}
	return append(events, events[6], events[0])
}

func Run(ctx context.Context, dir string, repeats int) (Suite, error) {
	return run(ctx, dir, repeats, nil)
}
func run(ctx context.Context, dir string, repeats int, beforeEffect func(context.Context, Effect) error) (Suite, error) {
	if ctx == nil || repeats < 1 || repeats > 5 {
		return Suite{}, errors.New("runtimeeval: context and 1–5 repetitions required")
	}
	events := Fixture()
	b, _ := json.Marshal(events)
	digest := sha256.Sum256(b)
	suite := Suite{Version: "m9b-serial-protocol-v1", Evidence: "real runtime entrypoints; deterministic in-process StreamModel script; no live provider", ContextBytes: ContextBytes, ScriptCallLimit: ScriptCallLimit, WorkerSlots: 1, FixtureDigest: hex.EncodeToString(digest[:]), Events: events,
		Limitations: []string{"Not the four-way M9 scheduling benchmark: serial input, two tasks, no cancellation, backlog, timing or stochastic model inference.", "Neutral and delegate-first prompts use identical scripted decisions; no prompt-policy effect is measured.", "Single-Pi subtask tool is synchronous and host-managed; its dedup/snapshots are in memory, not a durable SuperAgent replacement.", "No pre-M1 historical baseline, provider tokens, monetary cost or production/release acceptance."}}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Suite{}, err
	}
	for _, mode := range modes {
		for rep := 1; rep <= repeats; rep++ {
			if err := ctx.Err(); err != nil {
				return suite, err
			}
			path, err := os.MkdirTemp(dir, "runtimeeval-")
			if err != nil {
				return suite, err
			}
			s := newScript(ContextBytes, ScriptCallLimit)
			h := &host{script: s, beforeEffect: beforeEffect}
			result := Result{Mode: mode, Repetition: rep, Histories: map[string][]string{}}
			if mode == "superagent_subscribed" || mode == "superagent_router_only" {
				err = h.super(ctx, filepath.Join(path, "journal.jsonl"), events, &result)
			} else {
				err = h.single(ctx, events, &result)
			}
			if err != nil {
				return suite, fmt.Errorf("%s repetition %d: %w", mode, rep, err)
			}
			result.Effects = h.effects
			result.Metrics.ScriptCalls = s.calls()
			result.MainInputs = s.mainInputs
			result.Metrics.TaskCount = len(result.Histories)
			result.Metrics.RecordEffects = len(result.Effects)
			result.Metrics.WorkerInvocations = h.workerInvocations
			result.Metrics.WorkerToolCalls = h.workerToolCalls
			result.Metrics.SubtaskToolCalls = h.subtaskToolCalls
			result.Metrics.ToolCalls = h.workerToolCalls + h.subtaskToolCalls
			result.Metrics.Replies = len(result.Replies)
			for _, d := range result.Deliveries {
				if d.Duplicate {
					result.Metrics.Duplicates++
				} else {
					result.Metrics.UniqueEvents++
				}
			}
			if err := Validate(events, result); err != nil {
				return suite, fmt.Errorf("%s: %w", mode, err)
			}
			suite.Runs = append(suite.Runs, result)
		}
	}
	return suite, nil
}

type host struct {
	script            *script
	mu                sync.Mutex
	effects           []Effect
	workerInvocations int
	workerToolCalls   int
	subtaskToolCalls  int
	beforeEffect      func(context.Context, Effect) error
}

func (h *host) worker() (superagent.Worker, error) {
	tool := agent.ToolDefinition{Name: "record", Parameters: schema(`{"type":"object","properties":{"source":{"type":"string"},"event_id":{"type":"string"},"work_id":{"type":"string"},"value":{"type":"string"}},"required":["source","event_id","work_id","value"],"additionalProperties":false}`), Execute: func(ctx context.Context, tc agent.ToolExecutionContext) (agent.ToolResult, error) {
		var e Effect
		if err := json.Unmarshal(tc.ToolCall.Arguments, &e); err != nil {
			return agent.ToolResult{}, err
		}
		if h.beforeEffect != nil {
			if err := h.beforeEffect(ctx, e); err != nil {
				return agent.ToolResult{}, err
			}
		}
		if err := ctx.Err(); err != nil {
			return agent.ToolResult{}, err
		}
		h.mu.Lock()
		h.effects = append(h.effects, e)
		h.mu.Unlock()
		b, _ := json.Marshal(e)
		return agent.ToolResult{Content: []agent.Part{agent.NewTextPart(string(b))}}, nil
	}}
	worker, err := superagent.PiWorker(agent.AgentDefinition{Name: "worker", Model: h.script.model("worker"), Tools: []agent.ToolDefinition{tool}, MaxTurns: 2})
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, job superagent.Job) (superagent.Outcome, error) {
		h.mu.Lock()
		h.workerInvocations++
		h.mu.Unlock()
		out, err := worker(ctx, job)
		h.mu.Lock()
		for _, receipt := range out.Receipts {
			if receipt.ToolName == "record" {
				h.workerToolCalls++
			}
		}
		h.mu.Unlock()
		return out, err
	}, nil
}
func (h *host) super(ctx context.Context, path string, events []Event, result *Result) error {
	router, err := superagent.PiMainRouter(agent.AgentDefinition{Model: h.script.model("router")}, ContextBytes)
	if err != nil {
		return err
	}
	worker, err := h.worker()
	if err != nil {
		return err
	}
	a, err := superagent.OpenMain(ctx, path, 1, router, worker)
	if err != nil {
		return err
	}
	defer a.Close()
	workByTask := map[string]string{}
	for _, e := range events {
		var accepted bool
		if result.Mode == "superagent_subscribed" && e.Trigger.Source == "github" {
			accepted, err = a.SubmitThread(e.Trigger)
		} else {
			accepted, err = a.Submit(e.Trigger)
		}
		if err != nil {
			return err
		}
		if err = a.WaitIdle(ctx); err != nil {
			return err
		}
		d := Delivery{Source: e.Trigger.Source, EventID: e.Trigger.ID, Duplicate: !accepted}
		found := 0
		for _, report := range a.Status().Reports {
			if report.Source == d.Source && report.EventID == d.EventID {
				found++
				d.Status = report.Status
				d.WorkID = workByTask[report.TaskID]
				if e.Trigger.Kind == "request" && accepted {
					var effect Effect
					if err = json.Unmarshal([]byte(report.Text), &effect); err != nil {
						return err
					}
					workByTask[report.TaskID] = effect.WorkID
					d.WorkID = effect.WorkID
					if result.Mode == "superagent_subscribed" {
						if err = a.SetThreadSubscription(report.TaskID, "github", "pr-"+effect.WorkID, true); err != nil {
							return err
						}
					}
				}
				for _, receipt := range report.Receipts {
					if receipt.ToolName == "record" && receipt.Completed {
						d.Receipts++
					}
				}
			}
		}
		if found != 1 {
			return fmt.Errorf("event %s/%s has %d terminal reports", d.Source, d.EventID, found)
		}
		result.Deliveries = append(result.Deliveries, d)
	}
	if len(a.MainFailures()) != 0 {
		return errors.New("main session failed")
	}
	for task, work := range workByTask {
		snapshot, ok, e := a.TaskSnapshot(task)
		if e != nil {
			return e
		}
		if !ok {
			return errors.New("missing task snapshot")
		}
		result.Histories[work], err = history(snapshot)
		if err != nil {
			return err
		}
	}
	for _, reply := range a.Replies() {
		var effect Effect
		if err = json.Unmarshal([]byte(reply.Text), &effect); err != nil {
			return err
		}
		result.Replies = append(result.Replies, effect)
	}
	before := a.Status()
	replies := a.Replies()
	calls := h.script.calls()
	if err = a.Close(); err != nil {
		return err
	}
	a, err = superagent.OpenMain(ctx, path, 1, router, worker)
	if err != nil {
		return err
	}
	defer a.Close()
	for _, e := range events {
		var accepted bool
		if result.Mode == "superagent_subscribed" && e.Trigger.Source == "github" {
			accepted, err = a.SubmitThread(e.Trigger)
		} else {
			accepted, err = a.Submit(e.Trigger)
		}
		if err != nil {
			return err
		}
		if accepted {
			return errors.New("reopen accepted duplicate")
		}
	}
	if err = a.WaitIdle(ctx); err != nil {
		return err
	}
	if !reflect.DeepEqual(before, a.Status()) || !reflect.DeepEqual(replies, a.Replies()) || h.script.calls() != calls {
		return errors.New("reopen changed outcomes or repeated script")
	}
	result.ReopenChecked = true
	return nil
}

func (h *host) single(ctx context.Context, events []Event, result *Result) error {
	worker, err := h.worker()
	if err != nil {
		return err
	}
	snapshots := map[string]agent.AgentSnapshot{}
	outcomes := map[string]superagent.Outcome{}
	// PiWorker owns an independent receipt scope. Nested receipt wrappers in a
	// Runner tool context are intentionally unsupported. Use an explicit host
	// queue, with one host-owned worker and cancellation linked to the caller,
	// rather than erasing context values or changing the production contract.
	type response struct {
		outcome superagent.Outcome
		err     error
	}
	type request struct {
		ctx     context.Context
		trigger superagent.Trigger
		work    string
		done    chan response
	}
	queue := make(chan request)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for request := range queue {
			out, err := worker(request.ctx, superagent.Job{TaskID: request.work, ProviderSessionID: "single-worker-" + request.work, Trigger: request.trigger, Snapshot: snapshots[request.work]})
			if err == nil {
				snapshots[request.work] = out.Snapshot
				outcomes[key(request.trigger)] = out
			}
			request.done <- response{out, err}
		}
	}()
	defer func() { close(queue); <-done }()
	delegate := agent.ToolDefinition{Name: "subtask", Parameters: schema(`{"type":"object","properties":{"id":{"type":"string"},"source":{"type":"string"},"thread":{"type":"string"},"kind":{"type":"string"},"text":{"type":"string"}},"required":["id","source","thread","kind","text"],"additionalProperties":false}`), Execute: func(toolContext context.Context, tc agent.ToolExecutionContext) (agent.ToolResult, error) {
		var trigger superagent.Trigger
		if err := json.Unmarshal(tc.ToolCall.Arguments, &trigger); err != nil {
			return agent.ToolResult{}, err
		}
		var p Payload
		if err := json.Unmarshal([]byte(trigger.Text), &p); err != nil {
			return agent.ToolResult{}, err
		}
		requestContext, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(toolContext, cancel)
		defer func() { stop(); cancel() }()
		req := request{ctx: requestContext, trigger: trigger, work: p.WorkID, done: make(chan response, 1)}
		select {
		case queue <- req:
		case <-requestContext.Done():
			return agent.ToolResult{}, requestContext.Err()
		}
		// Wait for actual worker exit even after cancellation, just as PiWorker
		// does for admitted tool bodies. On error this probe aborts; it does
		// not export or persist failed-run receipts.
		res := <-req.done
		if res.err != nil {
			return agent.ToolResult{}, res.err
		}
		return agent.ToolResult{Content: []agent.Part{agent.NewTextPart(res.outcome.Summary)}}, nil
	}}
	prompt := "Handle requests with the available subtask tool when appropriate."
	if result.Mode == "single_pi_delegate_first" {
		prompt = "Prefer delegating actionable requests through the subtask tool."
	}
	runner, err := agent.NewRunner(agent.AgentDefinition{Name: "single-main", SystemPrompt: prompt, Model: h.script.model("single"), Tools: []agent.ToolDefinition{delegate}, MaxTurns: 2})
	if err != nil {
		return err
	}
	snapshot := agent.AgentSnapshot{SessionID: "single-main"}
	seen := map[string]superagent.Trigger{}
	for _, e := range events {
		d := Delivery{Source: e.Trigger.Source, EventID: e.Trigger.ID, Status: "ignored"}
		if prior, ok := seen[key(e.Trigger)]; ok {
			if prior != e.Trigger {
				return errors.New("conflicting duplicate")
			}
			d.Duplicate = true
		} else {
			seen[key(e.Trigger)] = e.Trigger
			// Match the host's deterministic noise admission, not an LLM decision.
			if e.Trigger.Kind != "noise" {
				b, _ := json.Marshal(e.Trigger)
				stream, handle, startErr := runner.RunWithReceipts(ctx, snapshot, []agent.Message{agent.NewUserTextMessage(string(b))}, agent.LoopHooks{}, 1)
				if startErr != nil {
					return startErr
				}
				for range stream.Events() {
				}
				snapshot, err = stream.Wait()
				if waitErr := handle.Wait(context.WithoutCancel(ctx)); waitErr != nil {
					return waitErr
				}
				for _, receipt := range handle.Snapshot() {
					if receipt.ToolName() == "subtask" {
						h.subtaskToolCalls++
					}
				}
				if err != nil {
					return err
				}
				var effect Effect
				if err = json.Unmarshal([]byte(textOf(snapshot.Messages[len(snapshot.Messages)-1])), &effect); err != nil {
					return err
				}
				result.Replies = append(result.Replies, effect)
			}
		}
		if out, ok := outcomes[key(e.Trigger)]; ok {
			var effect Effect
			if err = json.Unmarshal([]byte(out.Summary), &effect); err != nil {
				return err
			}
			d.WorkID = effect.WorkID
			d.Status = out.Status
			for _, receipt := range out.Receipts {
				if receipt.ToolName == "record" && receipt.Completed {
					d.Receipts++
				}
			}
		}
		result.Deliveries = append(result.Deliveries, d)
	}
	for work, snapshot := range snapshots {
		result.Histories[work], err = history(snapshot)
		if err != nil {
			return err
		}
	}
	return nil
}
func key(t superagent.Trigger) string { return t.Source + "/" + t.ID }
func history(snapshot agent.AgentSnapshot) ([]string, error) {
	var keys []string
	for _, m := range snapshot.Messages {
		if m.Role == agent.RoleUser {
			var t superagent.Trigger
			if err := json.Unmarshal([]byte(textOf(m)), &t); err != nil {
				return nil, err
			}
			keys = append(keys, key(t))
		}
	}
	return keys, nil
}

func schema(raw string) map[string]any {
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		panic(err)
	}
	return out
}
