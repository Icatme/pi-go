package superagent

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent/session"
)

// Inspect the immutable header before a writer has a chance to repair a torn
// tail. The state file and its directory belong to this trusted local host.
func openState(ctx context.Context, path string, create bool) (*SuperAgent, error) {
	header, err := session.ReadJSONLHeader(path)
	var store *session.JSONLStorage
	switch {
	case errors.Is(err, os.ErrNotExist) && create:
		store, err = session.CreateJSONLStorage(path, session.Header{ID: "superagent"})
	case err != nil:
		return nil, err
	case header.ID != "superagent":
		return nil, errors.New("superagent: wrong journal header")
	default:
		store, err = session.OpenJSONLStorage(path)
	}
	if err != nil {
		return nil, err
	}
	return restoreState(ctx, store)
}

func restoreState(ctx context.Context, store journalStore) (*SuperAgent, error) {
	ctx, cancel := context.WithCancel(ctx)
	a := &SuperAgent{watchdogRuntime: rand.Text(), ctx: ctx, cancel: cancel, store: store, changed: make(chan struct{}), accepted: make(map[[2]string]int), tasks: make(map[string]*task), subscriptions: make(map[[2]string]string), threadSubscriptions: make(map[[2]string][]string)}
	if err := a.restore(); err != nil {
		cancel()
		return nil, errors.Join(err, store.Close())
	}
	return a, nil
}

func (t *task) blocks(index int) bool {
	return t.review != 0 || t.backgroundReview != 0 || index < t.admitFrom || index < t.backgroundAdmitFrom
}

func validNote(note string) bool {
	return strings.TrimSpace(note) != "" && len(note) <= 4096 && utf8.ValidString(note)
}

// Reconcile records a trusted host's confirmation of a specific interrupted
// turn. Previously admitted deliveries stay blocked. Use a new event ID for
// fresh instructions. The confirmation note is stored in the private journal.
func (a *SuperAgent) Reconcile(taskID, source, eventID, note string) error {
	if !validNote(note) {
		return errors.New("reconciliation requires a nonempty note of at most 4096 UTF-8 bytes")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fatal != nil {
		return a.fatal
	}
	if err := a.ctx.Err(); err != nil {
		return err
	}
	t := a.tasks[taskID]
	if t == nil || t.review == 0 || t.running {
		return errors.New("task is unknown, running, or does not require review")
	}
	cause := a.events[t.review-1].trigger
	if cause.Source != source || cause.ID != eventID {
		return errors.New("reconciliation does not match the task's current review event")
	}
	return a.commit(transition{State: "reconciled", Index: t.review - 1, Task: taskID, Text: note})
}

// ReconcileFile exclusively opens an existing journal without dispatching any
// queued work. An active coordinator holds the same exclusive writer lock.
func ReconcileFile(path, taskID, source, eventID, note string) error {
	if !validNote(note) {
		return errors.New("reconciliation requires a nonempty note of at most 4096 UTF-8 bytes")
	}
	a, err := openState(context.Background(), path, false)
	if err != nil {
		return err
	}
	if err := a.recoverInterrupted(); err != nil {
		return errors.Join(err, a.Close())
	}
	err = a.Reconcile(taskID, source, eventID, note)
	return errors.Join(err, a.Close())
}
