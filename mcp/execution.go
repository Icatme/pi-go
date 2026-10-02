package mcp

import "context"

// ResponseError preserves an SDK-reported lifecycle result when its raw JSON
// or local projection was rejected. It is never permission to repeat a call.
type ResponseError struct {
	NeedsInput bool
	Err        error
}

func (e *ResponseError) Error() string {
	return "mcp: received response rejected; do not retry automatically"
}
func (e *ResponseError) Unwrap() error { return e.Err }

// Track observes a single SDK operation, including an error path. It exposes
// no credentials, arguments or result content. Cache hits send no new request.
func (c *Connection) Track(ctx context.Context) (context.Context, func() DispatchRecord) {
	return c.observer.Track(ctx)
}
