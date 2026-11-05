package superagent

import "errors"

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
}

// ReplyAttempt retains the facts of each send independently of its reply.
// started and unknown both leave the reply needs_review, including on restart.
type ReplyAttempt struct {
	Delivery  ReplyDelivery `json:"delivery"`
	Status    string        `json:"status"` // started, unknown, delivered, not_delivered
	MessageID string        `json:"message_id,omitempty"`
	Note      string        `json:"note,omitempty"`
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
	tr := transition{State: "reply_resolved", Main: &mainTransition{ReplyID: delivery.ReplyID, Delivery: &delivery, DeliveryResult: &result}}
	if err := a.validateMain(tr); err != nil {
		return err
	}
	previous := a.reply(delivery.ReplyID).Attempts[delivery.Attempt-1]
	if previous.Status == result.Status && previous.MessageID == result.MessageID && (previous.Note == result.Note || result.Status != "unknown") {
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
	case "reply_resolved":
		if m.Delivery == nil || m.Delivery.ReplyID != r.ID || m.Delivery.Attempt == 0 || m.Delivery.Attempt > uint64(len(r.Attempts)) || m.DeliveryResult == nil {
			return errors.New("superagent: unknown reply delivery")
		}
		result := *m.DeliveryResult
		if result.Note != "" && !validNote(result.Note) {
			return errors.New("superagent: invalid delivery note")
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
			if index, exists := a.accepted[[2]string{r.Source, result.MessageID}]; exists && a.events[index].trigger.Thread == r.Thread {
				return errors.New("superagent: platform message was already admitted as business input")
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
			if previous.Status != result.Status || previous.MessageID != result.MessageID {
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
	case "reply_resolved":
		previous := &r.Attempts[m.Delivery.Attempt-1]
		// Duplicate historical facts must not change a newer attempt or note.
		if previous.Status == "delivered" || previous.Status == "not_delivered" {
			return
		}
		previous.Status, previous.MessageID, previous.Note = m.DeliveryResult.Status, m.DeliveryResult.MessageID, m.DeliveryResult.Note
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
