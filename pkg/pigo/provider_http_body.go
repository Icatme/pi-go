package pigo

import (
	"io"
	"sync"
)

// providerObservedBody preserves read errors before an SDK can turn them into
// message text. It neither drains nor buffers the stream. Close remains directly
// delegated so that it can interrupt a blocked Read.
type providerObservedBody struct {
	io.ReadCloser
	observe     func(HTTPObservation)
	observation HTTPObservation
	once        sync.Once
}

func (b *providerObservedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF {
		b.once.Do(func() {
			observation := b.observation
			observation.Err = err
			b.observe(observation)
		})
	}
	return n, err
}
