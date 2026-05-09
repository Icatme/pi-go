package pigo

import (
	"context"
	"log/slog"
	"time"
)

type LoggingObserver struct {
	logger *slog.Logger
	level  slog.Level
}

func NewLoggingObserver(logger *slog.Logger, level slog.Level) *LoggingObserver {
	if logger == nil {
		logger = slog.Default()
	}
	return &LoggingObserver{logger: logger, level: level}
}

func (o *LoggingObserver) OnRequestStart(ctx context.Context, model Model, _ any) context.Context {
	o.logger.LogAttrs(ctx, o.level, "request start",
		slog.String("model", model.ID),
		slog.String("provider", string(model.Provider)),
		slog.String("api", string(model.API)),
	)
	return ctx
}

func (o *LoggingObserver) OnRequestComplete(ctx context.Context, model Model, result AssistantMessage, duration time.Duration) {
	attrs := []slog.Attr{
		slog.String("model", model.ID),
		slog.String("provider", string(model.Provider)),
		slog.String("stop_reason", string(result.StopReason)),
		slog.Int64("duration_ms", duration.Milliseconds()),
		slog.Int("token_input", result.Usage.Input),
		slog.Int("token_output", result.Usage.Output),
		slog.Int("cache_read", result.Usage.CacheRead),
		slog.Int("cache_write", result.Usage.CacheWrite),
		slog.Float64("cost", result.Usage.Cost.Total),
	}
	if result.ResponseID != "" {
		attrs = append(attrs, slog.String("response_id", result.ResponseID))
	}
	o.logger.LogAttrs(ctx, o.level, "request complete", attrs...)
}

func (o *LoggingObserver) OnRequestError(ctx context.Context, model Model, err error, duration time.Duration) {
	o.logger.LogAttrs(ctx, slog.LevelError, "request error",
		slog.String("model", model.ID),
		slog.String("provider", string(model.Provider)),
		slog.Int64("duration_ms", duration.Milliseconds()),
		slog.String("error", err.Error()),
	)
}

func (o *LoggingObserver) OnStreamEvent(_ context.Context, _ Model, _ AssistantMessageEvent) {
}

func (o *LoggingObserver) OnStreamFinish(ctx context.Context, model Model, _ AssistantMessage, eventCount int, droppedCount int, duration time.Duration) {
	if eventCount > 0 || droppedCount > 0 {
		o.logger.LogAttrs(ctx, slog.LevelDebug, "stream finish",
			slog.String("model", model.ID),
			slog.String("provider", string(model.Provider)),
			slog.Int("event_count", eventCount),
			slog.Int("dropped_count", droppedCount),
			slog.Int64("duration_ms", duration.Milliseconds()),
		)
	}
}
