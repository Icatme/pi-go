package superagent

import (
	"errors"
	"time"
)

// ErrReplyRateLimited means a verified non-delivery cannot be retried yet.
var ErrReplyRateLimited = errors.New("superagent: reply retry deadline has not elapsed")

// ReplyDelivery identifies one claimed external send. ReplyID is the stable
// transport idempotency key across retries; Attempt fences late callbacks.
type ReplyDelivery struct {
	ReplyID string `json:"reply_id"`
	Attempt uint64 `json:"attempt"`
}

// ReplyDeliveryResult is host-verified transport evidence, never model output.
// delivered requires the platform message ID. not_delivered requires a safe
// public Note confirming the send has ended without delivery; a timeout or a
// non-authoritative lookup is unknown, not proof that retrying is safe.
// Do not put credentials, raw errors or private platform data in Note.
type ReplyDeliveryResult struct {
	Status    string `json:"status"` // delivered, not_delivered, unknown
	MessageID string `json:"message_id,omitempty"`
	Note      string `json:"note,omitempty"`
	// Cursor is an opaque platform receipt cursor, not a global ordering claim.
	Cursor string `json:"cursor,omitempty"`
	// RetryAt is a Unix millisecond deadline, only for verified not_delivered.
	RetryAt int64 `json:"retry_at,omitempty"`
}

// ReplyAttempt retains the facts of each send independently of its reply.
// started and unknown both leave the reply needs_review, including on restart.
type ReplyAttempt struct {
	Delivery  ReplyDelivery `json:"delivery"`
	Status    string        `json:"status"` // started, unknown, delivered, not_delivered
	MessageID string        `json:"message_id,omitempty"`
	Note      string        `json:"note,omitempty"`
	Cursor    string        `json:"cursor,omitempty"`
	RetryAt   int64         `json:"retry_at,omitempty"`
}

func (a *SuperAgent) reply(id string) *Reply {
	for i := range a.main.replies {
		if a.main.replies[i].ID == id {
			return &a.main.replies[i]
		}
	}
	return nil
}

func (a *SuperAgent) replyReady() error {
	if a.fatal != nil {
		return a.fatal
	}
	return a.ctx.Err()
}

// BeginReplyDelivery atomically claims a pending reply before any external
// send. Only the caller receiving a nonzero handle may send. Until resolved,
// the reply is needs_review and cannot be claimed again. Use the same ReplyID
// as the platform idempotency key, not the attempt number.
func (a *SuperAgent) BeginReplyDelivery(id string) (ReplyDelivery, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.replyReady(); err != nil {
		return ReplyDelivery{}, err
	}
	r := a.reply(id)
	if r == nil {
		return ReplyDelivery{}, errors.New("superagent: unknown reply")
	}
	// Conservatively throttle this entire source/account namespace. Existing
	// in-flight sends cannot be recalled; new claims survive restart safely.
	now := time.Now().UnixMilli()
	for _, other := range a.main.replies {
		if other.Source != r.Source {
			continue
		}
		for _, attempt := range other.Attempts {
			if attempt.RetryAt > now {
				return ReplyDelivery{}, ErrReplyRateLimited
			}
		}
	}
	delivery := ReplyDelivery{ReplyID: id, Attempt: uint64(len(r.Attempts)) + 1}
	if err := a.commit(transition{State: "reply_claimed", Main: &mainTransition{ReplyID: id, Delivery: &delivery}}); err != nil {
		return ReplyDelivery{}, err
	}
	return delivery, nil
}

// ResolveReplyDelivery persists evidence for exactly one send. Unknown
// outcomes require later verification, never automatic resending. Only a
// verified not_delivered result makes the reply pending again. Conflicting
// terminal results fail without changing state. Repeated identical terminal
// facts are idempotent even after a later attempt has started.
func (a *SuperAgent) ResolveReplyDelivery(delivery ReplyDelivery, result ReplyDeliveryResult) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.resolveReplyDelivery(delivery, result)
}

func (a *SuperAgent) resolveReplyDelivery(delivery ReplyDelivery, result ReplyDeliveryResult) error {
	if err := a.replyReady(); err != nil {
		return err
	}
	state := "reply_resolved"
	if result.Cursor != "" || result.RetryAt != 0 {
		state = "reply_channel_resolved"
	}
	tr := transition{State: state, Main: &mainTransition{ReplyID: delivery.ReplyID, Delivery: &delivery, DeliveryResult: &result}}
	if err := a.validateMain(tr); err != nil {
		return err
	}
	previous := a.reply(delivery.ReplyID).Attempts[delivery.Attempt-1]
	if previous.Status == result.Status && previous.MessageID == result.MessageID && previous.RetryAt == result.RetryAt && (result.Cursor == "" || previous.Cursor == result.Cursor) && (previous.Note == result.Note || result.Status != "unknown") {
		return nil
	}
	return a.commit(tr)
}

// ObserveReply consumes a trusted adapter's delivery confirmation or own-message
// echo. The adapter must authenticate the sender/receipt and correlate it to the
// original handle; arbitrary message text is not proof. Source and Thread must
// match the host-owned reply destination. This never enters a Router or Worker.
// Use this before Submit when an own-message echo arrives before the send result.
func (a *SuperAgent) ObserveReply(source, thread string, delivery ReplyDelivery, messageID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	r := a.reply(delivery.ReplyID)
	if r == nil || r.Source != source || r.Thread != thread {
		return errors.New("superagent: reply observation destination mismatch")
	}
	return a.resolveReplyDelivery(delivery, ReplyDeliveryResult{Status: "delivered", MessageID: messageID})
}

// AcknowledgeReply confirms a direct in-memory read, with no external send.
// It is idempotent and never enters the main inbox. Once a delivery has been
// attempted, use its exact handle and ResolveReplyDelivery instead.
func (a *SuperAgent) AcknowledgeReply(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.replyReady(); err != nil {
		return err
	}
	tr := transition{State: "reply_read", Main: &mainTransition{ReplyID: id}}
	if err := a.validateMain(tr); err != nil {
		return err
	}
	if a.reply(id).Status == "acknowledged" {
		return nil
	}
	return a.commit(tr)
}

func (a *SuperAgent) validateReply(tr transition) error {
	m := tr.Main
	r := a.reply(m.ReplyID)
	if r == nil {
		return errors.New("superagent: unknown reply")
	}
	switch tr.State {
	case "reply_started": // M1a/M1b journal, not emitted by new writers.
		if r.Status != "pending" || len(r.Attempts) != 0 {
			return errors.New("invalid legacy reply start")
		}
	case "reply_acknowledged": // Legacy confirmation may lack a platform ID.
		if len(r.Attempts) > 1 {
			return errors.New("legacy acknowledgement cannot resolve a retry")
		}
	case "reply_read":
		if len(r.Attempts) != 0 || (r.Status != "pending" && r.Status != "acknowledged") {
			return errors.New("superagent: direct acknowledgement cannot resolve an external send")
		}
	case "reply_claimed":
		if r.Status != "pending" || m.Delivery == nil || m.Delivery.ReplyID != r.ID || m.Delivery.Attempt != uint64(len(r.Attempts))+1 || m.DeliveryResult != nil {
			return errors.New("superagent: reply is not pending or claim identity is invalid")
		}
	case "reply_resolved", "reply_channel_resolved":
		if m.Delivery == nil || m.Delivery.ReplyID != r.ID || m.Delivery.Attempt == 0 || m.Delivery.Attempt > uint64(len(r.Attempts)) || m.DeliveryResult == nil {
			return errors.New("superagent: unknown reply delivery")
		}
		result := *m.DeliveryResult
		if (tr.State == "reply_channel_resolved") != (result.Cursor != "" || result.RetryAt != 0) {
			return errors.New("platform receipt facts require channel journal transition")
		}
		if result.Note != "" && !validNote(result.Note) {
			return errors.New("superagent: invalid delivery note")
		}
		if (result.Cursor != "" && (!validNote(result.Cursor) || result.Status != "delivered")) || result.RetryAt < 0 || result.RetryAt > 253402300799999 || (result.RetryAt != 0 && result.Status != "not_delivered") {
			return errors.New("superagent: invalid delivery cursor or retry deadline")
		}
		switch result.Status {
		case "delivered":
			if !validIdentity(result.MessageID) {
				return errors.New("superagent: delivered reply requires a platform message identity")
			}
			for _, other := range a.main.replies {
				if other.Source != r.Source || other.Thread != r.Thread {
					continue
				}
				for _, attempt := range other.Attempts {
					if attempt.MessageID == result.MessageID && attempt.Delivery != *m.Delivery {
						return errors.New("superagent: platform message already belongs to another delivery")
					}
				}
			}
			for _, event := range a.events {
				if event.trigger.Source != r.Source || event.trigger.Thread != r.Thread {
					continue
				}
				messageID := event.trigger.ID
				if event.admissionRecord != nil {
					messageID = admissionMessageID(event.admissionRecord.Envelope)
				}
				if messageID == result.MessageID {
					return errors.New("superagent: platform message was already admitted as business input")
				}
			}
		case "not_delivered":
			if !validNote(result.Note) || result.MessageID != "" {
				return errors.New("superagent: non-delivery requires a verification note and no message identity")
			}
		case "unknown":
			if result.MessageID != "" {
				return errors.New("superagent: unknown delivery cannot bind a message identity")
			}
		default:
			return errors.New("superagent: invalid delivery result")
		}
		previous := r.Attempts[m.Delivery.Attempt-1]
		if previous.Status == "delivered" || previous.Status == "not_delivered" {
			if previous.Status != result.Status || previous.MessageID != result.MessageID || previous.RetryAt != result.RetryAt || (previous.Cursor != "" && result.Cursor != "" && previous.Cursor != result.Cursor) {
				return errors.New("superagent: conflicting terminal delivery result")
			}
		} else if m.Delivery.Attempt != uint64(len(r.Attempts)) || r.Status != "needs_review" {
			return errors.New("superagent: delivery does not own the current attempt")
		}
	default:
		return errors.New("unknown reply transition")
	}
	return nil
}

func (a *SuperAgent) applyReply(tr transition) {
	m := tr.Main
	r := a.reply(m.ReplyID)
	switch tr.State {
	case "reply_started":
		r.Attempts = append(r.Attempts, ReplyAttempt{Delivery: ReplyDelivery{ReplyID: r.ID, Attempt: 1}, Status: "started"})
		r.Status = "needs_review"
	case "reply_claimed":
		r.Attempts = append(r.Attempts, ReplyAttempt{Delivery: *m.Delivery, Status: "started"})
		r.Status = "needs_review"
	case "reply_acknowledged", "reply_read":
		r.Status = "acknowledged"
		if len(r.Attempts) > 0 {
			r.Attempts[len(r.Attempts)-1].Status = "delivered"
		}
	case "reply_resolved", "reply_channel_resolved":
		previous := &r.Attempts[m.Delivery.Attempt-1]
		// Identical terminal evidence may fill a missing cursor, never replace it
		// or change a newer attempt, retry deadline, or note.
		if previous.Status == "delivered" || previous.Status == "not_delivered" {
			if previous.Cursor == "" {
				previous.Cursor = m.DeliveryResult.Cursor
			}
			return
		}
		previous.Status, previous.MessageID, previous.Note = m.DeliveryResult.Status, m.DeliveryResult.MessageID, m.DeliveryResult.Note
		previous.Cursor, previous.RetryAt = m.DeliveryResult.Cursor, m.DeliveryResult.RetryAt
		switch previous.Status {
		case "delivered":
			r.Status = "acknowledged"
		case "not_delivered":
			r.Status = "pending"
		}
	}
}

// Match the original platform identity, never reply text or the source request
// ID. Hosts must normalize Trigger.ID to the message ID for own-message echoes.
func (a *SuperAgent) isReplyEcho(t Trigger) bool {
	for _, r := range a.main.replies {
		if r.Source != t.Source || r.Thread != t.Thread {
			continue
		}
		for _, attempt := range r.Attempts {
			if attempt.Status == "delivered" && attempt.MessageID != "" && attempt.MessageID == t.ID {
				return true
			}
		}
	}
	return false
}
