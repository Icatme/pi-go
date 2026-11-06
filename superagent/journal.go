package superagent

import (
	"context"
	"errors"
	"reflect"

	"github.com/Icatme/pi-go/agent/session"
)

// Journal stores the coordinator's ordered main-lane log. Hosts can implement
// this interface with their own storage and add LeasedJournal ownership for
// OpenMainJournal. All wire types come from the public agent/session package.
//
// Open and validate a coherent snapshot before passing the journal to the
// coordinator. Header must use session.HeaderKindSession,
// session.CurrentFormatVersion and ID "superagent", with no parent session.
// Log contains committed session.LogItemEntry records in sequence order.
// Entry IDs are decimal sequence numbers starting at 1; each ParentID names
// its predecessor, with an empty parent for the first entry.
//
// AppendEntry accepts session.MainLane and the coordinator's supplied ID and
// CustomData. Preserve the opaque transition payload and atomically assign the
// sequence, parent and timestamp. Return success only after durable commit,
// then include that entry in Log. Header, Log and AppendEntry return detached
// values. A host backend must surface initialization/read errors before handoff.
type Journal interface {
	Header() session.Header
	Log() []session.LogItem
	AppendEntry(string, session.NewEntry) (session.Entry, error)
	Close() error
}

// LeasedJournal is an exclusively owned, coherent coordinator journal snapshot.
// Every append must atomically verify the current unexpired ownership epoch and
// expected log head with the durable write. An expired owner cannot renew its
// old epoch. Context must be canceled when ownership is lost or uncertain.
// Header and Log return detached values. Close releases only this owner's epoch.
// A backend must stop accepting writes after any uncertain commit outcome.
//
// This is deliberately narrower than session.Storage: a coordinator only uses
// one ordered main-lane journal. A session's versioned CompareAppend alone does
// not fence external executors or provide cross-process ownership.
// Implementations are trusted host infrastructure, not model-selected objects.
type LeasedJournal interface {
	Journal
	Context() context.Context
}

// MainJournalOptions configures the existing coordinator on a leased journal.
// Set exactly one of Router and BatchRouter. Background remains an explicit
// host grant, including when reopening a journal with background operations.
type MainJournalOptions struct {
	Parallel     int
	Router       MainRouter
	BatchRouter  MainBatchRouter
	BatchOptions MainBatchOptions
	Worker       Worker
	Background   *BackgroundOptions
}

// OpenMainJournal starts a main coordinator on an already claimed journal
// supplied by the host. The journal can use sharedsqlite or an external
// database adapter that implements LeasedJournal. The host owns connection
// setup, schema migration and ownership acquisition. A lease covers the whole
// authorization domain; parallel workers run inside that owner.
//
// After argument validation this function takes ownership of journal, including
// on replay failure. Close waits for callbacks before closing the journal.
// Lease loss cancels callbacks and every later commit must also be fenced by
// the backend. Cancellation cannot undo external effects: interrupted work and
// unknown sends remain subject to the existing review/reconciliation barriers.
func OpenMainJournal(ctx context.Context, journal LeasedJournal, options MainJournalOptions) (*SuperAgent, error) {
	if ctx == nil || nilJournal(journal) || options.Parallel < 1 || options.Parallel > 64 || (options.Router == nil) == (options.BatchRouter == nil) || options.Worker == nil {
		return nil, errors.New("superagent: require a context, leased journal, 1–64 workers, main router and worker")
	}
	if err := validateBackgroundOptions(options.Background); err != nil {
		return nil, err
	}
	if options.BatchRouter != nil && (options.BatchOptions.MaxItems < 1 || options.BatchOptions.MaxItems > 64 || options.BatchOptions.MaxInputBytes < 1024 || options.BatchOptions.MaxInputBytes > 1<<20) {
		return nil, errors.New("superagent: main batch requires 1–64 items and 1 KiB–1 MiB input budget")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lifetime := journal.Context()
	if lifetime == nil {
		return nil, errors.New("superagent: journal requires an ownership context")
	}
	if err := lifetime.Err(); err != nil {
		return nil, errors.Join(err, journal.Close())
	}
	owned, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(lifetime, cancel)
	a, err := restoreState(owned, journal)
	if err != nil {
		stop()
		cancel()
		return nil, err
	}
	// Cancel both context levels and detach the watcher on Close/error. The
	// journal itself remains alive while a callback finishes its final receipt.
	innerCancel := a.cancel
	a.cancel = func() { innerCancel(); cancel() }
	a.stopJournalWatch = stop
	return startMain(a, options.Parallel, options.Router, options.BatchRouter, options.BatchOptions, options.Worker, options.Background)
}

func nilJournal(journal LeasedJournal) bool {
	if journal == nil {
		return true
	}
	value := reflect.ValueOf(journal)
	switch value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface, reflect.Chan:
		return value.IsNil()
	}
	return false
}
