// Package channel provides host-authenticated ingress and approved-reply
// delivery boundaries. It contains no network client, credentials, or live
// service integration. Hosts supply a truthful, authenticated Transport.
package channel

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Icatme/pi-go/superagent"
)

var ErrUncertain = errors.New("channel: delivery needs verification")

// Config binds one immutable protocol/account namespace to one coordinator's
// authorization domain. Source must equal string(Protocol)+":"+Organization,
// making the stored reply namespace bind the protocol/account after restart.
// Each coordinator journal still belongs to one host authorization domain.
type Config struct {
	Protocol     Protocol
	Source       string
	Organization string
	Policy       superagent.AdmissionPolicy
}

// Request contains only approved reply text encoded for this protocol, never
// raw Worker output. GitHub output creates PR discussion issue comments only,
// not inline review comments or review submissions. Delivery.Attempt fences callbacks; IdempotencyKey remains
// stable across verified retries. It does not assert platform idempotency.
type Request struct {
	Protocol       Protocol                 `json:"protocol"`
	Source         string                   `json:"source"`
	Organization   string                   `json:"organization"`
	Thread         string                   `json:"thread"`
	Delivery       superagent.ReplyDelivery `json:"delivery"`
	IdempotencyKey string                   `json:"idempotency_key"`
	Body           json.RawMessage          `json:"body"`
}

// Receipt is authenticated transport evidence, not untrusted callback JSON.
// not_delivered/rate_limited guarantee the attempt ended without creating a
// message, including no in-flight work. A timeout, absent search result, HTTP
// error, or malformed receipt is unknown. Only delivered may carry MessageID
// and Cursor. GitHub MessageID is the raw positive decimal issue-comment ID;
// it is persisted as issue_comment:<id>, distinct from review object IDs.
// rate_limited requires RetryAt, an absolute Unix-millisecond
// deadline. Conservatively all new sends for Source wait until that deadline.
type Receipt struct {
	Source       string                   `json:"source"`
	Organization string                   `json:"organization"`
	Thread       string                   `json:"thread"`
	Delivery     superagent.ReplyDelivery `json:"delivery"`
	Status       string                   `json:"status"` // delivered, not_delivered, rate_limited, unknown
	MessageID    string                   `json:"message_id,omitempty"`
	Cursor       string                   `json:"cursor,omitempty"`
	RetryAt      int64                    `json:"retry_at,omitempty"`
}

// Transport must authenticate platform replies and bind evidence to the exact
// request. Send is called once per claimed attempt. Lookup is read-only and
// MUST NOT resend: authoritative absence requires proof the original attempt
// ended, not a missing message in a paginated/eventually consistent listing.
// Implementations own network limits, escaping and platform API permissions.
type Transport interface {
	Send(context.Context, Request) (Receipt, error)
	Lookup(context.Context, Request) (Receipt, error)
}

// Adapter is safe for concurrent use when its host Transport is. It owns no
// poller, scheduler, retry loop, or account credentials. Do not mutate callbacks
// or rebind a Source while its coordinator journal exists.
type Adapter struct {
	agent     *superagent.SuperAgent
	config    Config
	transport Transport
}

func New(a *superagent.SuperAgent, config Config, transport Transport) (*Adapter, error) {
	if a == nil || transport == nil || (config.Protocol != IM && config.Protocol != GitHub) || !channelID(config.Source) || !channelID(config.Organization) || config.Source != string(config.Protocol)+":"+config.Organization || !channelID(config.Policy.Version) || len(config.Policy.Rules) > 1024 {
		return nil, errors.New("channel: invalid host configuration")
	}
	config.Policy.Rules = slices.Clone(config.Policy.Rules)
	for _, r := range config.Policy.Rules {
		if r.Source != config.Source || !channelID(r.Principal) || r.Group != config.Organization {
			return nil, errors.New("channel: policy crosses configured source or organization")
		}
	}
	return &Adapter{agent: a, config: config, transport: transport}, nil
}
func channelID(s string) bool {
	return ingressIdentity(s)
}

func channelThread(protocol Protocol, thread string) bool {
	if !channelID(thread) {
		return false
	}
	if protocol == IM {
		return true
	}
	parts := strings.Split(thread, "/")
	return len(parts) == 3 && parts[1] == "pull" && ingressDecimalID(json.Number(parts[0])) != "" && ingressDecimalID(json.Number(parts[2])) != ""
}

// Admit cross-checks payload identities against host-authenticated facts before
// invoking the coordinator's durable policy/dedupe boundary. Correlations are
// only the host's TaskID/ToThread, never links or task names inside message text.
func (a *Adapter) Admit(identity Identity, body []byte) (superagent.AdmissionReceipt, error) {
	if identity.Organization != a.config.Organization {
		return superagent.AdmissionReceipt{}, errors.New("channel: organization mismatch")
	}
	envelope, err := Normalize(a.config.Protocol, a.config.Source, identity, body)
	if err != nil {
		return superagent.AdmissionReceipt{}, err
	}
	return a.agent.Admit(a.config.Policy, envelope)
}

func (a *Adapter) request(id string) (Request, error) {
	for _, reply := range a.agent.Replies() {
		if reply.ID != id {
			continue
		}
		if reply.Source != a.config.Source || !channelThread(a.config.Protocol, reply.Thread) {
			return Request{}, errors.New("channel: reply destination mismatch")
		}
		// Protocol encoding preserves the Router-approved text byte-for-byte. A
		// platform-specific formatter must not summarize or append unapproved text.
		var body []byte
		var err error
		if a.config.Protocol == IM {
			body, err = json.Marshal(struct {
				Thread string `json:"thread_id"`
				Text   string `json:"text"`
			}{reply.Thread, reply.Text})
		} else {
			body, err = json.Marshal(struct {
				Body string `json:"body"`
			}{reply.Text})
		}
		if err != nil {
			return Request{}, errors.New("channel: reply encoding failed")
		}
		return Request{Protocol: a.config.Protocol, Source: a.config.Source, Organization: a.config.Organization, Thread: reply.Thread, Delivery: superagent.ReplyDelivery{ReplyID: reply.ID}, IdempotencyKey: reply.ID, Body: body}, nil
	}
	return Request{}, errors.New("channel: unknown approved reply")
}

// SendReply loads the immutable approved content/destination by ID, persists a
// claim before calling the transport, and records its outcome. Any error after
// the claim leaves a durable verification barrier, never an automatic retry.
func (a *Adapter) SendReply(ctx context.Context, id string) (superagent.ReplyDelivery, error) {
	if ctx == nil {
		return superagent.ReplyDelivery{}, errors.New("channel: context required")
	}
	if err := ctx.Err(); err != nil {
		return superagent.ReplyDelivery{}, err
	}
	request, err := a.request(id)
	if err != nil {
		return superagent.ReplyDelivery{}, err
	}
	request.Delivery, err = a.agent.BeginReplyDelivery(id)
	if err != nil {
		return superagent.ReplyDelivery{}, err
	}
	receipt, callErr := transportCall(ctx, a.transport, request, false)
	return request.Delivery, a.resolve(request, receipt, callErr)
}

// Reconcile performs only a lookup of an existing unresolved attempt. No retry
// occurs even if Lookup establishes non-delivery. The host must explicitly call
// SendReply again after any persisted RetryAt. Context cancellation is not proof.
func (a *Adapter) Reconcile(ctx context.Context, delivery superagent.ReplyDelivery) error {
	if ctx == nil {
		return errors.New("channel: context required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	request, err := a.request(delivery.ReplyID)
	if err != nil {
		return err
	}
	if err = a.unresolved(delivery); err != nil {
		return err
	}
	request.Delivery = delivery
	receipt, callErr := transportCall(ctx, a.transport, request, true)
	return a.resolve(request, receipt, callErr)
}
func (a *Adapter) unresolved(delivery superagent.ReplyDelivery) error {
	for _, r := range a.agent.Replies() {
		if r.ID != delivery.ReplyID {
			continue
		}
		if delivery.Attempt == 0 || delivery.Attempt != uint64(len(r.Attempts)) || r.Status != "needs_review" {
			break
		}
		status := r.Attempts[delivery.Attempt-1].Status
		if status == "started" || status == "unknown" {
			return nil
		}
	}
	return errors.New("channel: delivery is not unresolved")
}

// Observe records a host-authenticated asynchronous receipt. Call this before
// Admit for an early own-message echo, supplying the original delivery handle.
// This method never treats arbitrary inbound JSON as transport evidence.
func (a *Adapter) Observe(receipt Receipt) error {
	request, err := a.request(receipt.Delivery.ReplyID)
	if err != nil {
		return err
	}
	request.Delivery = receipt.Delivery
	if receipt.Source != request.Source || receipt.Organization != request.Organization || receipt.Thread != request.Thread {
		return errors.New("channel: receipt destination mismatch")
	}
	return a.resolve(request, receipt, nil)
}
func transportCall(ctx context.Context, t Transport, request Request, lookup bool) (receipt Receipt, err error) {
	defer func() {
		if recover() != nil {
			receipt = Receipt{}
			err = ErrUncertain
		}
	}()
	// The adapter retains an independent immutable request for receipt checking.
	request.Body = slices.Clone(request.Body)
	if lookup {
		return t.Lookup(ctx, request)
	}
	return t.Send(ctx, request)
}
func (a *Adapter) resolve(request Request, receipt Receipt, callErr error) error {
	result := superagent.ReplyDeliveryResult{Status: "unknown", Note: "Transport outcome requires verification."}
	uncertain := callErr != nil
	if !uncertain {
		if receipt.Source != request.Source || receipt.Organization != request.Organization || receipt.Thread != request.Thread || receipt.Delivery != request.Delivery {
			uncertain = true
		} else {
			switch receipt.Status {
			case "delivered":
				if !channelID(receipt.MessageID) || receipt.RetryAt != 0 || len(receipt.Cursor) > 4096 || !utf8.ValidString(receipt.Cursor) || (receipt.Cursor != "" && strings.TrimSpace(receipt.Cursor) == "") {
					uncertain = true
				} else {
					messageID := receipt.MessageID
					if a.config.Protocol == GitHub {
						if ingressDecimalID(json.Number(messageID)) == "" {
							uncertain = true
							break
						}
						messageID = "issue_comment:" + messageID
					}
					result = superagent.ReplyDeliveryResult{Status: "delivered", MessageID: messageID, Cursor: receipt.Cursor}
				}
			case "not_delivered", "rate_limited":
				if receipt.MessageID != "" || receipt.Cursor != "" || receipt.RetryAt < 0 || receipt.RetryAt > 253402300799999 || (receipt.Status == "rate_limited" && receipt.RetryAt == 0) {
					uncertain = true
				} else {
					result = superagent.ReplyDeliveryResult{Status: "not_delivered", Note: "Authenticated transport confirmed the attempt ended without delivery.", RetryAt: receipt.RetryAt}
				}
			case "unknown":
				uncertain = true
			default:
				uncertain = true
			}
		}
	}
	err := a.agent.ResolveReplyDelivery(request.Delivery, result)
	if uncertain {
		return errors.Join(ErrUncertain, err)
	}
	return err
}
