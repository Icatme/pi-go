// Command pi-superagent accepts local events and prints receipts and reports as JSONL.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/internal/jsontext"
	"github.com/Icatme/pi-go/pkg/pigo"
	"github.com/Icatme/pi-go/superagent"
)

const reconcileUsage = "pi-superagent reconcile -state PATH -task ID -source S -event ID -note TEXT"
const usage = "usage: pi-superagent run -state PATH [-workers N] [-provider NAME -model ID]\n       pi-superagent demo [-state PATH] [-workers N]\n       " + reconcileUsage

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := command(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func command(ctx context.Context, args []string, in io.Reader, out, diagnostics io.Writer) (err error) {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		_, err = fmt.Fprintln(out, usage)
		return err
	}
	if len(args) > 0 && args[0] == "reconcile" {
		return reconcile(args[1:], out, diagnostics)
	}
	if len(args) == 0 || (args[0] != "run" && args[0] != "demo") {
		return errors.New(usage)
	}
	demo := args[0] == "demo"
	opts := flag.NewFlagSet(args[0], flag.ContinueOnError)
	opts.SetOutput(diagnostics)
	opts.Usage = func() {
		fmt.Fprintln(diagnostics, usage)
		fmt.Fprintln(diagnostics, "REPORT replays each outcome and its tool receipts with a stable journal-local delivery_id. Deduplicate by journal namespace and delivery_id, not source/event_id.")
		fmt.Fprintln(diagnostics, "IDLE.tasks shows current review requirements after the input ends. This CLI retains legacy Open routing.")
		if !demo {
			fmt.Fprintln(diagnostics, controlHelp)
		}
		opts.PrintDefaults()
	}
	state := opts.String("state", "", "path to the local supervisor state file")
	workers := opts.Int("workers", 2, "maximum active workers")
	var provider, model string
	if !demo {
		opts.StringVar(&provider, "provider", "", "pi-go provider (requires -model)")
		opts.StringVar(&model, "model", "", "registered model ID (requires -provider)")
	}
	if err = opts.Parse(args[1:]); err != nil {
		return err
	}
	if opts.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", opts.Arg(0))
	}
	if *workers < 1 {
		return errors.New("-workers must be positive")
	}
	if *state == "" {
		if !demo {
			return errors.New("run requires -state PATH")
		}
		dir, err := os.MkdirTemp("", "pi-superagent-demo-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		*state = filepath.Join(dir, "state.json")
	}
	def, err := modelDefinition(provider, model)
	if err != nil {
		return err
	}
	route, err := superagent.PiRouter(def)
	if err != nil {
		return err
	}
	work, err := superagent.PiWorker(def)
	if err != nil {
		return err
	}
	s, err := superagent.Open(ctx, *state, *workers, route, work)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	// Replay persisted reports on each open. Consumers deduplicate REPORT by
	// (host journal namespace, delivery_id), including outcomes committed just
	// before a crash. Source/event identity is for ingress deduplication only.
	session := &outputSession{s: s, out: json.NewEncoder(out)}
	if demo {
		return session.demo(ctx)
	}
	return session.run(ctx, in)
}

func reconcile(args []string, out, diagnostics io.Writer) error {
	opts := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	opts.SetOutput(diagnostics)
	opts.Usage = func() {
		fmt.Fprintln(diagnostics, "usage:", reconcileUsage)
		fmt.Fprintln(diagnostics, "After checking the external effects, record your findings in the stopped supervisor's journal.")
		fmt.Fprintln(diagnostics, "To continue, start run and submit a fresh instruction with a new event ID.")
		opts.PrintDefaults()
	}
	state := opts.String("state", "", "existing local journal path")
	task := opts.String("task", "", "affected task ID from the report")
	source := opts.String("source", "", "source of the event requiring review")
	event := opts.String("event", "", "ID of the event requiring review")
	note := opts.String("note", "", "what the operator checked and found")
	if err := opts.Parse(args); err != nil {
		return err
	}
	if opts.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", opts.Arg(0))
	}
	for _, value := range []string{*state, *task, *source, *event, *note} {
		if strings.TrimSpace(value) == "" {
			return errors.New("reconcile requires -state, -task, -source, -event and a nonempty -note")
		}
	}
	if err := superagent.ReconcileFile(*state, *task, *source, *event, *note); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]string{
		"type": "RECONCILED", "task_id": *task, "source": *source, "event_id": *event,
		"next": "Submit a fresh instruction with a new event ID to continue this task.",
	})
}

func modelDefinition(provider, model string) (agent.AgentDefinition, error) {
	provider, model = strings.TrimSpace(provider), strings.TrimSpace(model)
	if provider == "" && model == "" {
		return agent.AgentDefinition{Model: superagent.OfflineModel()}, nil
	}
	if provider == "" || model == "" {
		return agent.AgentDefinition{}, errors.New("-provider and -model must be supplied together")
	}
	if pigo.GetModel(pigo.Provider(provider), model) == nil {
		return agent.AgentDefinition{}, fmt.Errorf("unknown pi-go model %s/%s", provider, model)
	}
	return agent.AgentDefinition{DefaultModel: agent.ModelRef{Provider: provider, Model: model}}, nil
}

type outputSession struct {
	s      *superagent.SuperAgent
	out    frameEncoder
	cursor int
}

func (s *outputSession) submit(event superagent.Trigger) error {
	accepted, err := s.s.Submit(event)
	if err != nil {
		return err
	}
	kind := "ACCEPTED"
	if !accepted {
		kind = "DUPLICATE"
	}
	return s.out.Encode(map[string]string{"type": kind, "source": event.Source, "thread": event.Thread, "event_id": event.ID})
}

func (s *outputSession) flush() error {
	reports := s.s.ReportsSince(s.cursor)
	if err := writeReports(s.out, reports); err != nil {
		// A partial write may have emitted some reports. Retry the batch with
		// its original IDs rather than skip an unacknowledged outcome.
		return err
	}
	s.cursor += len(reports)
	return nil
}

func writeReports(out frameEncoder, reports []superagent.Report) error {
	for _, r := range reports {
		if err := out.Encode(struct {
			Type string `json:"type"`
			superagent.Report
		}{"REPORT", r}); err != nil {
			return err
		}
	}
	return nil
}

func (s *outputSession) run(ctx context.Context, in io.Reader) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	frames := &frameQueue{ready: make(chan any, 128)}
	done := make(chan error, 1)
	go func() {
		live := &outputSession{s: s.s, out: frames, cursor: s.cursor}
		err := live.dispatch(ctx, in, frames)
		if frames.err != nil {
			// Staging itself failed. Stop work even if stdout is still blocked.
			err = errors.Join(err, s.s.Close())
		}
		frames.finish()
		done <- err
	}()
	// Only this goroutine touches the writer. Input dispatch never waits for
	// an Encode on stdout, including when replaying a previous run's reports.
	var outputErr error
	for frame := range frames.ready {
		if file, ok := frame.(*os.File); ok {
			if outputErr == nil {
				outputErr = replayFrames(s.out, file)
			}
			outputErr = errors.Join(outputErr, file.Close(), os.Remove(file.Name()))
		} else if outputErr == nil {
			outputErr = writeFrame(s.out, frame)
		}
		if outputErr != nil {
			cancel() // Drain and release any remaining files before returning.
		}
	}
	return errors.Join(outputErr, <-done)
}

func (s *outputSession) dispatch(ctx context.Context, in io.Reader, frames *frameQueue) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if closer, ok := in.(io.ReadCloser); ok {
		defer closer.Close()
	}
	type inputLine struct {
		line int
		raw  []byte
		err  error
	}
	lines := make(chan inputLine)
	go func(lines chan<- inputLine) {
		defer close(lines)
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for n := 1; ; n++ {
			var item inputLine
			item.line = n
			if scanner.Scan() {
				item.raw = bytes.Clone(scanner.Bytes())
			} else {
				if scanner.Err() == nil {
					return
				}
				item.err = scanner.Err()
			}
			select {
			case lines <- item:
			case <-ctx.Done():
				return
			}
			if item.err != nil {
				return
			}
		}
	}(lines)
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	var idle <-chan error
	var inputErr error
	controls := newControls(ctx, s)
	defer controls.close()
	flush := func() error {
		reports := s.s.ReportsSince(s.cursor)
		if len(reports) == 0 {
			return nil
		}
		if err := frames.Encode(reports); err != nil {
			return err
		}
		// This process-local cursor acknowledges staging, not delivery to the
		// external consumer. Restart replays the journal from count zero.
		s.cursor += len(reports)
		return nil
	}
	var idleDone bool
	for {
		select {
		case frames.spillReady() <- frames.spill:
			frames.spill = nil // stdout now owns and removes this file.
		case <-s.s.Done():
			// A failed journal append stops dispatch independently of stdin.
			// WaitIdle exposes the storage error and flush only emits synced outcomes.
			stopErr := s.s.WaitIdle(ctx)
			if stopErr == nil {
				stopErr = ctx.Err()
			}
			return errors.Join(inputErr, stopErr, controls.settle(ctx), flush())
		case <-ctx.Done():
			return errors.Join(inputErr, ctx.Err(), controls.settle(ctx), flush())
		case <-tick.C:
			if err := flush(); err != nil {
				return err
			}
		case event := <-controls.events:
			if err := controls.output(event); err != nil {
				return err
			}
		case err := <-idle:
			idle = nil
			if err != nil {
				return errors.Join(inputErr, err, controls.settle(ctx), flush())
			}
			idleDone = true
		case item, more := <-lines:
			if more {
				err := item.err
				if err == nil {
					control, parseErr := parseControl(item.raw, item.line)
					if control != nil {
						if err := controls.apply(*control, parseErr); err != nil {
							return err
						}
						continue
					}
					var trigger superagent.Trigger
					trigger, err = decodeTrigger(item.raw)
					if err == nil {
						err = s.submit(trigger)
					}
				}
				if err == nil {
					continue
				}
				if frames.err != nil {
					return frames.err
				}
				inputErr = fmt.Errorf("input line %d: %w", item.line, err)
			}
			// Drain work accepted before EOF or an input error. Keep delivering
			// its reports until the coordinator has finished persisting them.
			lines = nil
			done := make(chan error, 1)
			idle = done
			go func() { done <- s.s.WaitIdle(ctx) }()
		}
		// A steering receipt can resolve at the final poll or when the run exits.
		// Deliver that result before ending the stream, even if stdin is closed.
		if idleDone && len(controls.pending) == 0 {
			if err := flush(); err != nil {
				return errors.Join(inputErr, err)
			}
			return errors.Join(inputErr, s.idle())
		}
	}
}

func decodeTrigger(raw []byte) (superagent.Trigger, error) {
	var event superagent.Trigger
	if err := jsontext.ValidateUnicode(raw); err != nil {
		return event, fmt.Errorf("expected one UTF-8 JSON event per line: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return event, errors.New("expected a JSON event object")
	}
	fields := map[string]*string{"id": &event.ID, "source": &event.Source, "thread": &event.Thread, "kind": &event.Kind, "text": &event.Text}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return event, err
		}
		name := key.(string)
		dst := fields[name]
		if dst == nil {
			return event, fmt.Errorf("unknown or repeated event field %q", name)
		}
		val, err := decoder.Token()
		if err != nil {
			return event, err
		}
		value, ok := val.(string)
		if !ok {
			return event, fmt.Errorf("event field %q must be a string", name)
		}
		*dst = value
		delete(fields, name)
	}
	if len(fields) != 0 {
		return event, errors.New("event requires id, source, thread, kind and text")
	}
	return event, nil
}

func (s *outputSession) demo(ctx context.Context) error {
	a := superagent.Trigger{ID: "demo-1", Source: "console", Thread: "release", Kind: "request", Text: "Prepare the release checklist."}
	b := superagent.Trigger{ID: "demo-2", Source: "console", Thread: "support", Kind: "request", Text: "Summarize the support queue."}
	for _, event := range []superagent.Trigger{a, b, a} {
		if err := s.submit(event); err != nil {
			return err
		}
	}
	if err := s.s.WaitIdle(ctx); err != nil {
		return err
	}
	if err := s.flush(); err != nil {
		return err
	}
	var id string
	for _, r := range s.s.Status().Reports {
		if r.Source == a.Source && r.EventID == a.ID && r.TaskID != "" {
			id = r.TaskID
		}
	}
	if id == "" {
		return errors.New("demo release request produced no task")
	}
	for _, event := range []superagent.Trigger{
		{ID: "demo-3", Source: "build", Thread: "check", Kind: "signal", Text: "@" + id + " Build passed. Update the checklist."},
		{ID: "demo-4", Source: b.Source, Thread: b.Thread, Kind: "request", Text: "Add the latest support update."},
		{ID: "demo-5", Source: "monitor", Thread: "heartbeat", Kind: "noise", Text: "Heartbeat."},
	} {
		if err := s.submit(event); err != nil {
			return err
		}
	}
	if err := s.s.WaitIdle(ctx); err != nil {
		return err
	}
	if err := s.flush(); err != nil {
		return err
	}
	return s.idle()
}

func (s *outputSession) idle() error {
	return s.out.Encode(struct {
		Type  string                  `json:"type"`
		Tasks []superagent.TaskStatus `json:"tasks"`
	}{"IDLE", s.s.Status().Tasks})
}
