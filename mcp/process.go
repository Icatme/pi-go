package mcp

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
)

type managedProcess struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	stderr     io.ReadCloser
	stderrDone chan struct{}
	stderrErr  error // published by closing stderrDone
	waitErr    error // published by closing done
	control    *processControl
	done       chan struct{}
	once       sync.Once
	stopMu     sync.Mutex
	stop       func() bool
	closeErr   error
}

// SDK IOTransport and process teardown share these same owned pipes. A single
// physical close makes concurrent shutdown idempotent. exec.Cmd.Wait may have
// already closed its own pipe on process exit; that specific state is complete
// cleanup, while all other close failures remain visible.
type processReadPipe struct {
	io.ReadCloser
	once sync.Once
	err  error
}

func (p *processReadPipe) Close() error {
	p.once.Do(func() {
		p.err = p.ReadCloser.Close()
		if errors.Is(p.err, os.ErrClosed) {
			p.err = nil
		}
	})
	return p.err
}

type processWritePipe struct {
	io.WriteCloser
	once sync.Once
	err  error
}

func (p *processWritePipe) Close() error {
	p.once.Do(func() {
		p.err = p.WriteCloser.Close()
		if errors.Is(p.err, os.ErrClosed) {
			p.err = nil
		}
	})
	return p.err
}

// stderr is drained under a bounded retention cap; it is never included in
// model-visible errors or wire data. Process exit is reported separately.
type boundedStderr struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (b *boundedStderr) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if n >= b.limit {
		b.data = append(b.data[:0], p[n-b.limit:]...)
	} else {
		drop := len(b.data) + n - b.limit
		if drop > 0 {
			copy(b.data, b.data[drop:])
			b.data = b.data[:len(b.data)-drop]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}

func startManagedProcess(ctx context.Context, s ServerConfig, stderrLimit int) (*managedProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.Command(s.Command, s.Args...)
	cmd.Dir = s.Dir
	cmd.Env = append(os.Environ(), s.Env...)
	control, err := prepareProcess(cmd)
	if err != nil {
		return nil, err
	}
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		return nil, errors.Join(err, control.close())
	}
	cmd.Stderr = stderrWriter
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, errors.Join(err, stderr.Close(), stderrWriter.Close(), control.close())
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.Join(err, stdin.Close(), stderr.Close(), stderrWriter.Close(), control.close())
	}
	if err := cmd.Start(); err != nil {
		return nil, errors.Join(err, stdin.Close(), stdout.Close(), stderr.Close(), stderrWriter.Close(), control.close())
	}
	if err := control.attach(cmd.Process); err != nil {
		killErr := cmd.Process.Kill()
		waitErr := cmd.Wait()
		return nil, errors.Join(err, killErr, waitErr, stdin.Close(), stdout.Close(), stderr.Close(), stderrWriter.Close(), control.close())
	}
	p := &managedProcess{cmd: cmd, stdin: &processWritePipe{WriteCloser: stdin}, stdout: &processReadPipe{ReadCloser: stdout}, stderr: &processReadPipe{ReadCloser: stderr}, control: control, done: make(chan struct{}), stderrDone: make(chan struct{})}
	// Drain separately from exec.Cmd.Wait: an inherited stderr pipe must not
	// conceal the owned server's exit until its descendants also exit.
	go func() {
		_, p.stderrErr = io.Copy(&boundedStderr{limit: stderrLimit}, stderr)
		if errors.Is(p.stderrErr, os.ErrClosed) {
			p.stderrErr = nil
		}
		close(p.stderrDone)
	}()
	go func() { p.waitErr = cmd.Wait(); close(p.done) }()
	if err := stderrWriter.Close(); err != nil {
		return nil, errors.Join(err, p.close())
	}
	p.stopMu.Lock()
	p.stop = context.AfterFunc(ctx, func() { _ = p.close() })
	p.stopMu.Unlock()
	return p, nil
}

func (p *managedProcess) close() error {
	p.once.Do(func() {
		p.stopMu.Lock()
		if p.stop != nil {
			p.stop()
		}
		p.stopMu.Unlock()
		stdinErr := p.stdin.Close()
		stdoutErr := p.stdout.Close()
		killErr := p.control.kill(p.cmd.Process)
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		stderrErr := p.stderr.Close()
		<-p.stderrDone
		p.closeErr = errors.Join(stdinErr, stdoutErr, stderrErr, p.stderrErr, killErr, p.control.close())
		<-p.done
	})
	return p.closeErr
}
