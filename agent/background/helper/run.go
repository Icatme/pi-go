package helper

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/background"
)

// NativeConfig belongs in an explicitly installed host helper binary. No tool,
// permissions, credentials or executable is obtained from the request file.
type NativeConfig struct {
	Scope, PolicyRevision string
	Options               agent.RunToolCallOptions
	// Authorize checks the exact immutable operation's live authority. It is
	// composed with native CheckToolPermission, including pre-effect rechecks.
	// Revocation also cancels execution. Cancellation cannot undo prior effects.
	Authorize    func(context.Context, background.Request, uint64) error
	PollInterval time.Duration
}

// Run admits one native invocation, after a durable exclusive claim marker.
// A second invocation of Run never resumes/retries that operation. Call from
// the helper's main with its last argument. Do not run detached descendants:
// only receipt-captured native bodies belong to this lifecycle guarantee.
// Run waits for those bodies even after cancellation or a storage failure.
func Run(ctx context.Context, dir string, cfg NativeConfig) error {
	if runtime.GOOS != "linux" || ctx == nil || !filepath.IsAbs(dir) || privateDir(dir) != nil || !validID(cfg.Scope) || !validID(cfg.PolicyRevision) || cfg.Authorize == nil {
		return ErrInvalid
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 50 * time.Millisecond
	}
	if cfg.PollInterval < time.Millisecond || cfg.PollInterval > time.Second {
		return ErrInvalid
	}
	var m manifest
	if readJSON(filepath.Join(dir, "request.json"), &m) != nil || m.Version != 1 || m.Epoch == 0 || len(m.Grant) != 64 || m.PolicyRevision != cfg.PolicyRevision || m.Request.Scope != cfg.Scope || !validID(m.Request.ID) || !validID(m.Request.Call.ID) {
		return ErrInvalid
	}
	authorize := func(ctx context.Context) (err error) {
		defer func() {
			if recover() != nil {
				err = ErrDenied
			}
		}()
		if ctx.Err() != nil || cfg.Authorize(ctx, clone(m.Request), m.Epoch) != nil {
			return ErrDenied
		}
		return nil
	}
	nativePermission := cfg.Options.CheckToolPermission
	cfg.Options.CheckToolPermission = func(ctx context.Context, tc agent.BeforeToolCallContext) error {
		if err := authorize(ctx); err != nil {
			return err
		}
		if nativePermission != nil {
			return nativePermission(ctx, tc)
		}
		return nil
	}
	root, cancel := context.WithCancel(ctx)
	defer cancel()
	local, err := background.NewLocal(root, cfg.Scope, cfg.Options, 1)
	if err != nil {
		return ErrInvalid
	}
	d := digest(m)
	// Any claim creation/sync error is uncertain. Never remove/repair the marker.
	if writeNew(filepath.Join(dir, "claim.json"), d) != nil {
		return ErrUnavailable
	}
	stopped := func() bool {
		path := filepath.Join(dir, "stop.json")
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return false
		}
		// Any marker, including a corrupt/unreadable one, requests cancellation.
		// Its contents are verified by the backend before acknowledging Stop.
		return true
	}
	if authorize(root) != nil || stopped() {
		return replaceJSON(filepath.Join(dir, "observation.json"), snapshot{Digest: d, Observation: background.Observation{ID: m.Request.ID, State: "exited", Failed: true}})
	}
	if err := local.Start(root, m.Request); err != nil {
		return ErrUnavailable
	}
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	var saved background.Observation
	var storageErr error
	for {
		if root.Err() != nil || stopped() || authorize(root) != nil {
			cancel()
			_ = local.Stop(context.Background(), m.Request.ID)
		}
		o, err := local.Inspect(context.Background(), m.Request.ID)
		if err != nil {
			cancel()
			return ErrUnavailable
		}
		if storageErr == nil && !reflect.DeepEqual(saved, o) {
			if err := replaceJSON(filepath.Join(dir, "observation.json"), snapshot{Digest: d, Observation: o}); err != nil {
				storageErr = ErrUnavailable
				cancel()
			} else {
				saved = o
			}
		}
		if o.State == "exited" {
			return storageErr
		}
		<-ticker.C
	}
}
