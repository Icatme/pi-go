package pigo

import (
	"context"
	"io"
	"sync"
)

// A custom RoundTripper need not wire its response body to the request context.
// Own that cancellation at the adapter boundary as well as closing on return.
type cancelableProviderBody struct {
	body     io.ReadCloser
	ctx      context.Context
	once     sync.Once
	stop     func() bool
	closeErr error
}

func bindProviderResponseBody(ctx context.Context, body io.ReadCloser) *cancelableProviderBody {
	b := &cancelableProviderBody{body: body, ctx: ctx}
	b.stop = context.AfterFunc(ctx, func() { b.closeUnderlying() })
	return b
}

func (b *cancelableProviderBody) closeUnderlying() {
	b.once.Do(func() { b.closeErr = b.body.Close() })
}

func (b *cancelableProviderBody) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := b.body.Read(p)
	if err != nil && b.ctx.Err() != nil {
		err = b.ctx.Err()
	}
	return n, err
}

func (b *cancelableProviderBody) Close() error {
	b.stop()
	b.closeUnderlying()
	return b.closeErr
}
