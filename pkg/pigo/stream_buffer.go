package pigo

import (
	"context"
	"errors"
	"reflect"
)

const (
	assistantMessageEventPendingLimit = 2048
	assistantMessageEventByteBudget   = 32 << 20
)

// ErrStreamBufferExceeded means event delivery could not preserve lifecycle
// events within the stream's pending-buffer budget. Result retains provider
// evidence but reports StopReasonError rather than successful partial delivery.
var ErrStreamBufferExceeded = errors.New("stream event buffer limit exceeded")

func (s *AssistantMessageEventStream) enqueueLocked(event AssistantMessageEvent) context.CancelFunc {
	bytes := assistantEventBytes(event)
	droppable := isDroppableAssistantMessageEvent(event.Type)
	if droppable && bytes > assistantMessageEventByteBudget {
		s.droppedDelta++
		return nil
	}
	for (droppable && s.pendingDelta >= assistantMessageEventDeltaBuffer) ||
		len(s.pending) >= assistantMessageEventPendingLimit ||
		bytes > assistantMessageEventByteBudget-s.pendingBytes {
		if !s.dropOldestPendingDeltaLocked() {
			break
		}
	}
	if droppable && (len(s.pending) >= assistantMessageEventPendingLimit || bytes > assistantMessageEventByteBudget-s.pendingBytes) {
		// Preserve lifecycle events when only the incoming delta can be omitted.
		s.droppedDelta++
		return nil
	}
	if len(s.pending) >= assistantMessageEventPendingLimit || bytes > assistantMessageEventByteBudget-s.pendingBytes {
		s.bufferErr = ErrStreamBufferExceeded
		dropped := len(s.pending) + 1 + s.droppedDelta
		clear(s.pending)
		s.pending = nil
		s.pendingDelta, s.pendingBytes, s.droppedDelta = 0, 0, 0
		// Reserve one compact terminal error, never a synthetic successful Done.
		failed := AssistantMessageEvent{
			Type: AssistantMessageEventError, Reason: StopReasonError,
			Error:         AssistantMessage{StopReason: StopReasonError, ErrorMessage: s.bufferErr.Error()},
			DroppedEvents: dropped,
		}
		size := assistantEventBytes(failed)
		s.pending = append(s.pending, queuedAssistantMessageEvent{event: failed, bytes: size})
		s.pendingBytes = size
		s.queueCond.Signal()
		return s.cancelRequest
	}
	queued := queuedAssistantMessageEvent{event: cloneAssistantMessageEvent(event), droppable: droppable, bytes: bytes}
	s.pending = append(s.pending, queued)
	s.pendingBytes += bytes
	if droppable {
		s.pendingDelta++
	}
	s.queueCond.Signal()
	return nil
}

// assistantEventBytes conservatively charges inline data and reachable payloads,
// counting shared snapshots more than once. It is a payload budget, not a heap/RSS
// measurement; the public channel, one in-flight event and final result are separate.
// Bounded traversal avoids JSON serialization and stops at the budget, including
// for recursive or excessively deep custom tool arguments.
func assistantEventBytes(event AssistantMessageEvent) int {
	remaining := assistantMessageEventByteBudget
	chargeEventValue(reflect.ValueOf(event), &remaining, 0)
	if remaining < 0 {
		return assistantMessageEventByteBudget + 1
	}
	return assistantMessageEventByteBudget - remaining
}

func chargeEventValue(value reflect.Value, remaining *int, depth int) {
	if !value.IsValid() || *remaining < 0 {
		return
	}
	if depth > 64 || value.Type().Size() > uintptr(*remaining) {
		*remaining = -1
		return
	}
	*remaining -= int(value.Type().Size())
	switch value.Kind() {
	case reflect.String:
		*remaining -= value.Len()
	case reflect.Interface, reflect.Pointer:
		if !value.IsNil() {
			chargeEventValue(value.Elem(), remaining, depth+1)
		}
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice {
			// Cloning can preserve capacity, so charge unused backing storage too.
			elemSize := value.Type().Elem().Size()
			if elemSize != 0 && uintptr(value.Cap()) > uintptr(*remaining)/elemSize {
				*remaining = -1
				return
			}
			*remaining -= int(uintptr(value.Cap()) * elemSize)
		}
		for index := 0; index < value.Len() && *remaining >= 0; index++ {
			chargeEventValue(value.Index(index), remaining, depth+1)
		}
	case reflect.Map:
		iterator := value.MapRange()
		for iterator.Next() && *remaining >= 0 {
			// Include a conservative per-entry allowance for the map's storage.
			*remaining -= 32
			chargeEventValue(iterator.Key(), remaining, depth+1)
			chargeEventValue(iterator.Value(), remaining, depth+1)
		}
	case reflect.Struct:
		for index := 0; index < value.NumField() && *remaining >= 0; index++ {
			if value.Type().Field(index).IsExported() {
				chargeEventValue(value.Field(index), remaining, depth+1)
			}
		}
	}
}
