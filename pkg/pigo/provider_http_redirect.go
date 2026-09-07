package pigo

import (
	"errors"
	"io"
	"net/http"
	"sync"
)

// Redirect bodies must stay lazy so http.Client can follow a redirect without
// waiting for an error page it would otherwise close without reading.
type providerRedirectBody struct {
	body        io.ReadCloser
	policy      ProviderHTTPPolicy
	observation HTTPObservation

	readMu      sync.Mutex
	prefix      []byte
	done        bool
	terminalErr error
	closeOnce   sync.Once
	closeErr    error
}

func newProviderRedirectBody(body io.ReadCloser, policy ProviderHTTPPolicy, observation HTTPObservation) io.ReadCloser {
	return &providerRedirectBody{body: body, policy: policy, observation: observation}
}

func (b *providerRedirectBody) Read(p []byte) (int, error) {
	n, err, observation := b.read(p)
	b.observe(observation)
	return n, err
}

func (b *providerRedirectBody) read(p []byte) (int, error, *HTTPObservation) {
	b.readMu.Lock()
	defer b.readMu.Unlock()
	if b.done {
		return 0, b.terminalErr, nil
	}
	if len(p) == 0 {
		return 0, nil, nil
	}

	remaining := b.policy.MaxErrorBodyBytes - int64(len(b.prefix))
	if remaining == 0 {
		var probe [1]byte
		n, err := b.body.Read(probe[:])
		if n > 0 {
			observation := b.finishLocked(err, true)
			if err == nil {
				err = io.EOF
			}
			b.terminalErr = err
			return 0, err, observation
		}
		if err != nil {
			return 0, err, b.finishLocked(err, false)
		}
		return 0, nil, nil
	}

	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := b.body.Read(p)
	b.prefix = append(b.prefix, p[:n]...)
	if err != nil {
		return n, err, b.finishLocked(err, false)
	}
	return n, nil, nil
}

func (b *providerRedirectBody) closeBody() error {
	b.closeOnce.Do(func() { b.closeErr = b.body.Close() })
	return b.closeErr
}

// finishLocked runs once under readMu. Only EOF proves that the captured prefix
// is the complete body; an early Close must not identify a partial JSON prefix.
func (b *providerRedirectBody) finishLocked(readErr error, truncated bool) *HTTPObservation {
	closeErr := b.closeBody()
	b.done = true
	b.terminalErr = readErr
	if b.terminalErr == nil {
		b.terminalErr = http.ErrBodyReadAfterClose
	}
	observation := b.observation
	observation.ErrorBodyTruncated = truncated
	if !truncated && readErr == io.EOF {
		observation.ErrorType, observation.ErrorCode = providerErrorIdentifiers(b.prefix)
	}
	if readErr == io.EOF {
		readErr = nil
	}
	var limitErr error
	if truncated {
		limitErr = ErrProviderErrorBodyTooLarge
	}
	observation.Err = errors.Join(limitErr, readErr, closeErr)
	return &observation
}

func (b *providerRedirectBody) Close() error {
	// Closing the original body before waiting for readMu lets net/http unblock
	// a concurrent network Read. Holding readMu here would deadlock that path.
	closeErr := b.closeBody()
	b.readMu.Lock()
	var observation *HTTPObservation
	if !b.done {
		observation = b.finishLocked(nil, false)
	}
	b.readMu.Unlock()
	b.observe(observation)
	return closeErr
}

func (b *providerRedirectBody) observe(observation *HTTPObservation) {
	if observation != nil && b.policy.Observe != nil {
		b.policy.Observe(*observation)
	}
}
