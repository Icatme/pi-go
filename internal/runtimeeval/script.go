package runtimeeval

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/superagent"
)

type script struct {
	mu              sync.Mutex
	n, bytes, limit int
	mainInputs      map[string]int
}

func newScript(bytes, limit int) *script {
	return &script{bytes: bytes, limit: limit, mainInputs: map[string]int{}}
}
func (s *script) calls() int { s.mu.Lock(); defer s.mu.Unlock(); return s.n }
func (s *script) model(role string) agent.StreamModel {
	return agent.StreamFunc(func(ctx context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// One identical serialized request bound across all roles/arms. No truncation,
		// token estimate, credential discovery or real-provider fallback exists here.
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
		s.mu.Unlock()
		if len(req.Messages) == 0 {
			return nil, errors.New("script requires input")
		}
		last := req.Messages[len(req.Messages)-1]
		var answer string
		switch role {
		case "router", "router_batch":
			var input struct {
				Input  superagent.MainInput   `json:"input"`
				Inputs []superagent.MainInput `json:"inputs"`
				Tasks  []superagent.TaskInfo  `json:"tasks"`
			}
			if err = json.Unmarshal([]byte(textOf(last)), &input); err != nil {
				return nil, err
			}
			if role == "router" {
				decision, e := s.mainDecision(input.Input, input.Tasks)
				if e != nil {
					return nil, e
				}
				b, _ := json.Marshal(decision)
				answer = string(b)
			} else {
				decisions := make([]superagent.MainInputDecision, 0, len(input.Inputs))
				for _, item := range input.Inputs {
					decision, e := s.mainDecision(item, input.Tasks)
					if e != nil {
						return nil, e
					}
					decisions = append(decisions, superagent.MainInputDecision{InputID: item.ID, Decision: decision})
				}
				// Return reverse order to exercise identity-based batch correlation.
				for i, j := 0, len(decisions)-1; i < j; i, j = i+1, j-1 {
					decisions[i], decisions[j] = decisions[j], decisions[i]
				}
				b, _ := json.Marshal(struct {
					Decisions []superagent.MainInputDecision `json:"decisions"`
				}{decisions})
				answer = string(b)
			}
		case "worker", "single":
			if last.Role == agent.RoleTool {
				if last.ToolResult == nil || last.ToolResult.IsError {
					return nil, errors.New("script received tool error: " + textOf(last))
				}
				answer = textOf(last)
			} else if last.Role == agent.RoleUser {
				var trigger superagent.Trigger
				if err = json.Unmarshal([]byte(textOf(last)), &trigger); err != nil {
					return nil, err
				}
				call := agent.ToolCall{ID: "call-" + trigger.Source + "-" + trigger.ID}
				if role == "single" {
					call.Name = "subtask"
					call.Arguments = json.RawMessage(textOf(last))
				} else {
					var p Payload
					if err = json.Unmarshal([]byte(trigger.Text), &p); err != nil {
						return nil, err
					}
					call.Name = "record"
					call.Arguments, _ = json.Marshal(Effect{trigger.Source, trigger.ID, p.WorkID, p.Value})
				}
				message := agent.NewTextMessage(agent.RoleAssistant, "")
				message.StopReason = agent.StopReasonToolUse
				message.ToolCalls = []agent.ToolCall{call}
				return streamReply(message), nil
			} else {
				return nil, errors.New("unsupported script message")
			}
		default:
			return nil, errors.New("unknown script role")
		}
		message := agent.NewTextMessage(agent.RoleAssistant, answer)
		message.StopReason = agent.StopReasonStop
		return streamReply(message), nil
	})
}
func textOf(message agent.Message) string {
	var text string
	for _, p := range message.Parts {
		if p.Type == agent.PartTypeText {
			text += p.Text
		}
	}
	return text
}

type replyStream struct {
	message agent.Message
	events  chan agent.AssistantEvent
}

func streamReply(message agent.Message) agent.AssistantStream {
	events := make(chan agent.AssistantEvent, 1)
	events <- agent.AssistantEvent{Type: agent.AssistantEventDone, Message: message}
	close(events)
	return &replyStream{message, events}
}
func (s *replyStream) Events() <-chan agent.AssistantEvent { return s.events }
func (s *replyStream) Wait() (agent.Message, error)        { return s.message, nil }
func (s *replyStream) Close() error                        { return nil }

func (s *script) mainDecision(input superagent.MainInput, tasks []superagent.TaskInfo) (superagent.MainDecision, error) {
	s.mu.Lock()
	s.mainInputs[input.Kind]++
	s.mu.Unlock()
	decision := superagent.MainDecision{Action: "ignore"}
	switch input.Kind {
	case "worker_result":
		if input.Report == nil || input.Report.Status != "completed" || input.Report.NeedsReview {
			return decision, errors.New("script refuses a false success reply")
		}
		decision = superagent.MainDecision{Action: "reply", Text: input.Report.Text}
	case "user":
		var p Payload
		if err := json.Unmarshal([]byte(input.Trigger.Text), &p); err != nil {
			return decision, err
		}
		if input.Trigger.Kind == "request" {
			decision.Action = "new"
		} else if input.Trigger.Kind != "noise" {
			for _, task := range tasks {
				var prior Effect
				if json.Unmarshal([]byte(task.Summary), &prior) == nil && prior.WorkID == p.WorkID {
					decision = superagent.MainDecision{Action: "attach", TaskID: task.ID}
					break
				}
			}
			if decision.Action != "attach" {
				return decision, errors.New("script cannot resolve offered task")
			}
		}
	case "task_input": // Informational fast-path observation, never a new request.
	default:
		return decision, errors.New("unsupported main input")
	}
	return decision, nil
}
