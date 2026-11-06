package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Icatme/pi-go/internal/jsontext"
	"github.com/Icatme/pi-go/superagent"
)

const controlHelp = `While run is reading stdin, send {"op":"status"} for task IDs and active source/event IDs.
Use subscribe or unsubscribe with task_id for lossy local progress; these never change durable Thread routing subscriptions. Use steer or abort with task_id, source and event_id from the active run; steer also takes text.
Use ack_reply with reply_id only after directly consuming a REPLY. It is idempotent, not an external-platform delivery receipt, and cannot resolve an external send attempt.
Control frames may include request_id (up to 256 UTF-8 bytes). Responses carry input_line for correlation. STEER_QUEUED is followed by STEER_DELIVERED or STEER_NOT_DELIVERED when the receipt settles.`

const maxWaitingSteers = 128

type controlCommand struct {
	op, requestID, task, source, event, text, replyID string
	line                                              int
}

// An object with an op is a control frame. Other input retains the trigger
// parser's validation and fail-after-drain behavior.
func parseControl(raw []byte, line int) (*controlCommand, error) {
	if !hasControlOp(raw) {
		return nil, nil
	}
	c := &controlCommand{line: line}
	if err := jsontext.ValidateUnicode(raw); err != nil {
		return c, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if _, err := d.Token(); err != nil {
		return c, err
	}
	values := make(map[string]string)
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return c, err
		}
		name := key.(string)
		if _, dup := values[name]; dup {
			return c, fmt.Errorf("repeated control field %q", name)
		}
		token, err := d.Token()
		if err != nil {
			return c, err
		}
		value, ok := token.(string)
		if !ok {
			return c, fmt.Errorf("control field %q must be a string", name)
		}
		values[name] = value
	}
	if len(values["request_id"]) > 256 {
		return c, errors.New("request_id exceeds 256 UTF-8 bytes")
	}
	c.requestID = values["request_id"]
	for _, name := range []string{"task_id", "source", "event_id", "reply_id"} {
		if len(values[name]) > 256 {
			return c, fmt.Errorf("control field %q exceeds 256 UTF-8 bytes", name)
		}
	}
	c.op, c.task, c.source, c.event, c.text = values["op"], values["task_id"], values["source"], values["event_id"], values["text"]
	c.replyID = values["reply_id"]
	var required []string
	switch c.op {
	case "status":
	case "ack_reply":
		required = []string{"reply_id"}
	case "subscribe", "unsubscribe":
		required = []string{"task_id"}
	case "abort":
		required = []string{"task_id", "source", "event_id"}
	case "steer":
		required = []string{"task_id", "source", "event_id", "text"}
	default:
		c.op = ""
		return c, errors.New("unknown control operation")
	}
	for name := range values {
		if name != "op" && name != "request_id" && !slices.Contains(required, name) {
			return c, fmt.Errorf("unexpected %q field for %s", name, c.op)
		}
	}
	for _, name := range required {
		if strings.TrimSpace(values[name]) == "" {
			return c, fmt.Errorf("missing %q for %s", name, c.op)
		}
	}
	return c, nil
}

// An identifiable control remains recoverable even when the rest of its JSON
// is malformed. A line with no identifiable op keeps the trigger error policy.
func hasControlOp(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return false
	}
	var skip json.RawMessage
	for d.More() {
		key, err := d.Token()
		if err != nil {
			break
		}
		if key == "op" {
			return true
		}
		if d.Decode(&skip) != nil {
			break
		}
	}
	return false
}

type controlResponse struct {
	Type      string                   `json:"type"`
	Op        string                   `json:"op,omitempty"`
	InputLine int                      `json:"input_line"`
	RequestID string                   `json:"request_id,omitempty"`
	TaskID    string                   `json:"task_id,omitempty"`
	Source    string                   `json:"source,omitempty"`
	EventID   string                   `json:"event_id,omitempty"`
	ReplyID   string                   `json:"reply_id,omitempty"`
	Error     string                   `json:"error,omitempty"`
	Tasks     *[]superagent.TaskStatus `json:"tasks,omitempty"`
	Progress  *superagent.Progress     `json:"progress,omitempty"`
}

func (c controlCommand) response(kind string) controlResponse {
	return controlResponse{Type: kind, Op: c.op, InputLine: c.line, RequestID: c.requestID, TaskID: c.task, Source: c.source, EventID: c.event, ReplyID: c.replyID}
}

func (c controlCommand) progress(p superagent.Progress) controlResponse {
	r := c.response("PROGRESS")
	r.Progress = &p
	return r
}

type pendingSteer struct {
	reply   controlResponse
	receipt *superagent.SteerReceipt
}

func (p pendingSteer) wait(ctx context.Context) controlResponse {
	r := p.reply
	delivered, err := p.receipt.Wait(ctx)
	if err != nil {
		r.Type, r.Error = "STEER_UNCONFIRMED", err.Error()
	} else if delivered {
		r.Type = "STEER_DELIVERED"
	}
	return r
}

type taskSubscription struct {
	line   int
	cancel context.CancelFunc
}

type controls struct {
	ctx           context.Context
	session       *outputSession
	events        chan controlResponse
	subscriptions map[string]taskSubscription
	pending       map[int]pendingSteer // input line -> admission awaiting its final response
}

func newControls(ctx context.Context, s *outputSession) *controls {
	return &controls{ctx: ctx, session: s, events: make(chan controlResponse), subscriptions: make(map[string]taskSubscription), pending: make(map[int]pendingSteer)}
}

func (c *controls) close() {
	for _, sub := range c.subscriptions {
		sub.cancel()
	}
}

// The CLI goroutine exclusively owns the encoder and all subscription state.
// Forwarders wait here, outside the supervisor's workers and scheduler.
func (c *controls) forward(ctx context.Context, r controlResponse) {
	select {
	case c.events <- r:
	case <-ctx.Done():
	}
}

func (c *controls) apply(cmd controlCommand, parseErr error) error {
	reject := func(err error) error {
		r := cmd.response("CONTROL_ERROR")
		r.Error = err.Error()
		return c.session.out.Encode(r)
	}
	if parseErr != nil {
		return reject(parseErr)
	}
	target := superagent.RunTarget{TaskID: cmd.task, Source: cmd.source, EventID: cmd.event}
	switch cmd.op {
	case "ack_reply":
		if err := c.session.s.AcknowledgeReply(cmd.replyID); err != nil {
			return reject(err)
		}
		return c.session.out.Encode(cmd.response("REPLY_ACKNOWLEDGED"))
	case "status":
		tasks := c.session.s.Status().Tasks
		if tasks == nil {
			tasks = []superagent.TaskStatus{}
		}
		r := cmd.response("STATUS")
		r.Tasks = &tasks
		return c.session.out.Encode(r)
	case "abort":
		if err := c.session.s.Abort(target); err != nil {
			return reject(err)
		}
		return c.session.out.Encode(cmd.response("ABORT_REQUESTED"))
	case "steer":
		if len(c.pending) >= maxWaitingSteers {
			return reject(errors.New("too many pending steering receipts on this connection"))
		}
		receipt, err := c.session.s.Steer(target, cmd.text)
		if err != nil {
			return reject(err)
		}
		p := pendingSteer{reply: cmd.response("STEER_NOT_DELIVERED"), receipt: receipt}
		c.pending[cmd.line] = p
		go func() {
			c.forward(c.ctx, p.wait(c.ctx))
		}()
		return c.session.out.Encode(cmd.response("STEER_QUEUED"))
	case "subscribe":
		if _, exists := c.subscriptions[cmd.task]; exists {
			return reject(errors.New("task already subscribed on this connection"))
		}
		ctx, cancel := context.WithCancel(c.ctx)
		ch, err := c.session.s.Subscribe(ctx, cmd.task)
		if err != nil {
			cancel()
			return reject(err)
		}
		// Subscribe preloads the task's current state. Queue it with the
		// acknowledgement so EOF cannot overtake the initial observation.
		initial, ok := <-ch
		if !ok {
			cancel()
			return reject(errors.New("task subscription ended before initial progress"))
		}
		if err := c.session.out.Encode(cmd.response("SUBSCRIBED")); err != nil {
			cancel()
			return err
		}
		if err := c.session.out.Encode(cmd.progress(initial)); err != nil {
			cancel()
			return err
		}
		c.subscriptions[cmd.task] = taskSubscription{line: cmd.line, cancel: cancel}
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case p, ok := <-ch:
					if !ok {
						return
					}
					c.forward(ctx, cmd.progress(p))
				}
			}
		}()
		return nil
	case "unsubscribe":
		sub, ok := c.subscriptions[cmd.task]
		if !ok {
			return reject(errors.New("task is not subscribed on this connection"))
		}
		delete(c.subscriptions, cmd.task)
		sub.cancel()
		return c.session.out.Encode(cmd.response("UNSUBSCRIBED"))
	default:
		return reject(errors.New("unknown control operation"))
	}
}

func (c *controls) output(r controlResponse) error {
	if r.Type == "PROGRESS" {
		// A pending update can race with unsubscribe and a new subscription.
		if current, ok := c.subscriptions[r.TaskID]; !ok || current.line != r.InputLine {
			return nil
		}
	} else if _, ok := c.pending[r.InputLine]; !ok {
		return nil
	}
	if err := c.session.out.Encode(r); err != nil {
		return err
	}
	if r.Type != "PROGRESS" {
		delete(c.pending, r.InputLine)
	}
	return nil
}

// A stopped coordinator resolves its outstanding admissions independently
// of the CLI's forwarders. Read those receipts before closing stdout.
func (c *controls) settle(ctx context.Context) error {
	for _, p := range c.pending {
		if err := c.output(p.wait(ctx)); err != nil {
			return err
		}
	}
	return nil
}
