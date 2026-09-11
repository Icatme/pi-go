package pigo

import "context"

// managedAssistantStream owns a child request context. Watch the caller context,
// not a provider's per-attempt context: normal provider completion must still
// allow a slow consumer to read all remaining events.
func managedAssistantStream(parent context.Context, start func(context.Context) *AssistantMessageEventStream) *AssistantMessageEventStream {
	if parent == nil {
		parent = context.Background()
	}
	requestCtx, cancel := context.WithCancel(parent)
	stream := start(requestCtx)
	stream.queueMu.Lock()
	if stream.deliveryClosed {
		stream.queueMu.Unlock()
		cancel()
		return stream
	}
	stream.cancelRequest = cancel
	stream.stopContext = context.AfterFunc(parent, stream.Close)
	overflowed := stream.bufferErr != nil
	stream.queueMu.Unlock()
	if overflowed {
		cancel()
	}
	return stream
}

// Close cancels the owned provider request, discards unread events and waits for
// the event dispatcher to exit. It is safe to call concurrently and repeatedly.
// It does not fabricate a provider result or wait for provider/observer callbacks;
// Result still waits for the provider's final message and preserves its evidence.
func (s *AssistantMessageEventStream) Close() {
	s.releaseDelivery()
	<-s.dispatcherDone
	// Only the dispatcher sends/closes events. Once it exits, release the small
	// public channel's final buffered reference even if the caller retains s.
	for range s.events {
	}
}

func (s *AssistantMessageEventStream) releaseDelivery() {
	s.queueMu.Lock()
	if !s.deliveryClosed {
		s.deliveryClosed = true
		close(s.deliveryDone)
	}
	clear(s.pending)
	s.pending = nil
	s.pendingDelta = 0
	s.pendingBytes = 0
	s.queueCond.Broadcast()
	stop, cancel := s.stopContext, s.cancelRequest
	s.stopContext, s.cancelRequest = nil, nil
	s.queueMu.Unlock()
	// A context callback may already be running Close. Do not wait for it here.
	if stop != nil {
		stop()
	}
	if cancel != nil {
		cancel()
	}
}
