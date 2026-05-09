package pigo

import (
	"context"
	"time"
)

type RequestObserver interface {
	OnRequestStart(ctx context.Context, model Model, payload any) context.Context
	OnRequestComplete(ctx context.Context, model Model, result AssistantMessage, duration time.Duration)
	OnRequestError(ctx context.Context, model Model, err error, duration time.Duration)
}

type StreamObserver interface {
	OnStreamEvent(ctx context.Context, model Model, event AssistantMessageEvent)
	OnStreamFinish(ctx context.Context, model Model, final AssistantMessage, eventCount int, droppedCount int, duration time.Duration)
}

type Observer interface {
	RequestObserver
	StreamObserver
}
