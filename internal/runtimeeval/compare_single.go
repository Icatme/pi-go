package runtimeeval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/superagent"
)

type compareSingleInput struct {
	Input superagent.MainInput  `json:"input"`
	Tasks []superagent.TaskInfo `json:"tasks"`
}
type compareDelegation struct {
	Input    superagent.MainInput    `json:"input"`
	Decision superagent.MainDecision `json:"decision"`
}

func (s *script) compareSingleModel() agent.StreamModel {
	return agent.StreamFunc(func(ctx context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(req)
		if err != nil {
			return nil, err
		}
		if len(raw) > s.bytes {
			return nil, errors.New("script request byte budget exceeded")
		}
		s.mu.Lock()
		if s.n >= s.limit {
			s.mu.Unlock()
			return nil, errors.New("script call budget exceeded")
		}
		s.n++
		s.roleCalls["single_async"]++
		s.mu.Unlock()
		if len(req.Messages) == 0 {
			return nil, errors.New("missing script input")
		}
		last := req.Messages[len(req.Messages)-1]
		var answer string
		if last.Role == agent.RoleTool {
			if last.ToolResult == nil || last.ToolResult.IsError {
				return nil, errors.New("subtask admission failed")
			}
			answer = textOf(last)
		} else if last.Role == agent.RoleUser {
			var in compareSingleInput
			if err = json.Unmarshal([]byte(textOf(last)), &in); err != nil {
				return nil, err
			}
			decision, err := s.mainDecision(in.Input, in.Tasks)
			if err != nil {
				return nil, err
			}
			if decision.Action == "new" || decision.Action == "attach" {
				args, _ := json.Marshal(compareDelegation{in.Input, decision})
				message := agent.NewTextMessage(agent.RoleAssistant, "")
				message.StopReason = agent.StopReasonToolUse
				message.ToolCalls = []agent.ToolCall{{ID: "subtask-" + in.Input.ID, Name: "subtask", Arguments: args}}
				return streamReply(message), nil
			}
			if decision.Action != "reply" {
				return nil, errors.New("unexpected single main action")
			}
			answer = decision.Text
		} else {
			return nil, errors.New("unsupported single main message")
		}
		message := agent.NewTextMessage(agent.RoleAssistant, answer)
		message.StopReason = agent.StopReasonStop
		return streamReply(message), nil
	})
}

type compareResponse struct {
	job     superagent.Job
	outcome superagent.Outcome
	err     error
}

func (c *compareHost) single(ctx context.Context, events []Event, r *CompareResult, options compareOptions) error {
	ctx, cancel := context.WithCancel(ctx)
	worker, err := c.worker()
	if err != nil {
		cancel()
		return err
	}
	// One job per task per phase; the host does not implement overlapping-task
	// scheduling, cancellation/recovery policy, or a second production coordinator.
	queue := make(chan superagent.Job, 20)
	results := make(chan compareResponse, 20)
	var wg sync.WaitGroup
	for range CompareWorkerSlots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-queue:
					if !ok {
						return
					}
					out, e := worker(ctx, job)
					results <- compareResponse{job, out, e}
				}
			}
		}()
	}
	defer func() { cancel(); close(queue); wg.Wait() }()
	snapshots := map[string]agent.AgentSnapshot{}
	summaries := map[string]string{}
	admitted := 0
	delegate := agent.ToolDefinition{Name: "subtask", Parameters: schema(`{"type":"object","properties":{"input":{"type":"object"},"decision":{"type":"object"}},"required":["input","decision"],"additionalProperties":false}`), Execute: func(toolContext context.Context, tc agent.ToolExecutionContext) (agent.ToolResult, error) {
		var request compareDelegation
		if e := json.Unmarshal(tc.ToolCall.Arguments, &request); e != nil {
			return agent.ToolResult{}, e
		}
		input, decision := request.Input, request.Decision
		task := decision.TaskID
		if decision.Action == "new" {
			task = "single-task-" + input.ID
			if _, ok := snapshots[task]; ok {
				return agent.ToolResult{}, errors.New("duplicate task")
			}
			snapshots[task] = agent.AgentSnapshot{}
		} else if decision.Action != "attach" {
			return agent.ToolResult{}, errors.New("invalid delegation")
		}
		snapshot, ok := snapshots[task]
		if !ok {
			return agent.ToolResult{}, errors.New("unknown attachment task")
		}
		job := superagent.Job{TaskID: task, DeliveryID: input.DeliveryID, ProviderSessionID: "single-worker-" + task, Trigger: input.Trigger, Snapshot: snapshot}
		// The async job belongs to the host run, not the already-returned tool's
		// receipt scope/context. Whole-run cancellation is joined by the deferred pool.
		select {
		case queue <- job:
		case <-toolContext.Done():
			return agent.ToolResult{}, toolContext.Err()
		case <-ctx.Done():
			return agent.ToolResult{}, ctx.Err()
		}
		admitted++
		if options.admitted != nil {
			options.admitted(admitted)
		}
		acknowledgment := fmt.Sprintf("admitted:%s:%s", input.DeliveryID, task)
		return agent.ToolResult{Content: []agent.Part{agent.NewTextPart(acknowledgment)}}, nil
	}}
	prompt := "Handle requests with the available subtask tool when appropriate."
	if r.Mode == compareModes[3] {
		prompt = "Prefer delegating actionable requests through the subtask tool."
	}
	runner, err := agent.NewRunner(agent.AgentDefinition{Name: "single-main", SystemPrompt: prompt, Model: c.h.script.compareSingleModel(), Tools: []agent.ToolDefinition{delegate}, MaxTurns: 2})
	if err != nil {
		return err
	}
	snapshot := agent.AgentSnapshot{SessionID: "single-main"}
	runMain := func(input superagent.MainInput) (string, error) {
		var tasks []superagent.TaskInfo
		// Stable order; summary content, not an oracle or host WorkID lookup,
		// supplies the script's attachment decision.
		for _, task := range sortedCompareKeys(summaries) {
			tasks = append(tasks, superagent.TaskInfo{ID: task, Summary: summaries[task]})
		}
		raw, _ := json.Marshal(compareSingleInput{input, tasks})
		r.Inputs = append(r.Inputs, input)
		stream, handle, e := runner.RunWithReceipts(ctx, snapshot, []agent.Message{agent.NewUserTextMessage(string(raw))}, agent.LoopHooks{}, 1)
		if e != nil {
			return "", e
		}
		for range stream.Events() {
		}
		snapshot, e = stream.Wait()
		if joinErr := handle.Wait(context.WithoutCancel(ctx)); joinErr != nil {
			return "", joinErr
		}
		if e != nil {
			return "", e
		}
		output := textOf(snapshot.Messages[len(snapshot.Messages)-1])
		for _, receipt := range handle.Snapshot() {
			if receipt.ToolName() == "subtask" {
				c.h.subtaskToolCalls++
				r.Acknowledgments = append(r.Acknowledgments, CompareAck{InputID: input.ID, DeliveryID: input.DeliveryID, Text: output, Receipt: superagent.ToolReceipt{Token: receipt.Token(), ParentToken: receipt.ParentToken(), Domain: receipt.Domain(), CallID: receipt.CallID(), ToolName: receipt.ToolName(), Committed: receipt.Committed(), Completed: receipt.Completed()}})
			}
		}
		return output, nil
	}
	seen := map[string]superagent.Trigger{}
	accept := func(t superagent.Trigger) (bool, error) {
		if prior, ok := seen[key(t)]; ok {
			if prior != t {
				return false, errors.New("conflicting duplicate")
			}
			return false, nil
		}
		seen[key(t)] = t
		return true, nil
	}
	nextInput := 0
	for phase := 0; phase < 3; phase++ {
		ingress := phaseTriggers(events, phase)
		work := 0
		for _, t := range ingress {
			ok, e := accept(t)
			if e != nil {
				return e
			}
			r.Deliveries = append(r.Deliveries, LoadDelivery{t.Source, t.ID, ok})
			if !ok {
				continue
			}
			delivery := "single-delivery-" + key(t)
			if t.Kind == "noise" {
				r.Reports = append(r.Reports, superagent.Report{DeliveryID: delivery, Source: t.Source, Thread: t.Thread, EventID: t.ID, Status: "ignored"})
				continue
			}
			nextInput++
			input := superagent.MainInput{ID: fmt.Sprintf("single-input-%d", nextInput), DeliveryID: delivery, Kind: "user", Trigger: t}
			if _, e = runMain(input); e != nil {
				return e
			}
			work++
		}
		for range work {
			var response compareResponse
			select {
			case response = <-results:
			case <-ctx.Done():
				return ctx.Err()
			}
			if response.err != nil {
				return response.err
			}
			job, out := response.job, response.outcome
			snapshots[job.TaskID] = out.Snapshot
			summaries[job.TaskID] = out.Summary
			var effect Effect
			if err = json.Unmarshal([]byte(out.Summary), &effect); err != nil {
				return err
			}
			r.TaskWork[job.TaskID] = effect.WorkID
			report := superagent.Report{DeliveryID: job.DeliveryID, Source: job.Trigger.Source, EventID: job.Trigger.ID, Thread: job.Trigger.Thread, TaskID: job.TaskID, Status: out.Status, Text: out.Summary, Receipts: out.Receipts, NeedsReview: out.NeedsReview}
			r.Reports = append(r.Reports, report)
			nextInput++
			input := superagent.MainInput{ID: fmt.Sprintf("single-input-%d", nextInput), DeliveryID: job.DeliveryID, Kind: "worker_result", Trigger: job.Trigger, TaskID: job.TaskID, Report: &report}
			output, e := runMain(input)
			if e != nil {
				return e
			}
			r.Replies = append(r.Replies, superagent.Reply{ID: "single-final-" + input.ID, InputID: input.ID, DecisionID: "single-decision-" + input.ID, DeliveryID: job.DeliveryID, Source: report.Source, Thread: report.Thread, EventID: report.EventID, TaskID: report.TaskID, Text: output, Status: "returned_undelivered", Report: &report})
		}
	}
	conflict := events[0].Trigger
	conflict.Text += " conflicting duplicate"
	if ok, e := accept(conflict); ok || e == nil {
		return errors.New("conflicting duplicate accepted")
	}
	r.ConflictChecked = true
	for task, snap := range snapshots {
		r.Histories[r.TaskWork[task]], err = history(snap)
		if err != nil {
			return err
		}
	}
	return nil
}
