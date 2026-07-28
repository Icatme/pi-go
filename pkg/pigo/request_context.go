package pigo

import (
	"context"
	"time"
)

func providerRequestContext(parent context.Context, timeoutMs int) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if timeoutMs <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, time.Duration(timeoutMs)*time.Millisecond)
}
