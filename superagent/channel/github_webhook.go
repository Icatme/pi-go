package channel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"

	"github.com/Icatme/pi-go/superagent"
)

// ErrGitHubReplay means these exact signed bytes have already been admitted (or
// matched a known own echo). It does not establish that two deliveries are the
// same event. The host must retain the returned evidence and investigate any
// claimed independent event; the wrapper never silently admits it again.
var ErrGitHubReplay = errors.New("channel: signed GitHub body already observed; verify delivery")

// GitHubWebhookEvent is signed body evidence plus UNSIGNED event/delivery
// headers. GitHub HMAC authenticates bytes, not delivery freshness or authority.
// RawBody is detached for each host callback; it is not persisted by this wrapper.
// EventID for admission is github-body:<BodySHA256>, not the unsigned DeliveryID.
type GitHubWebhookEvent struct {
	Organization, Principal, Thread, MessageID string
	EventType, DeliveryID, Action, BodySHA256  string
	RawBody                                    []byte
}

// GitHubAuthorization is an independent host decision, never extracted from
// mentions, URLs, author_association, a signature or model-generated content.
// Zero values deny. The coordinator's actor policy is checked independently.
type GitHubAuthorization struct {
	Allowed         bool
	ExplicitRequest bool
	TaskID          string
	ToThread        bool
}

// GitHubWebhookConfig binds exact repository-ID/pull/number targets. Empty
// AllowedThreads denies everything. Secret supplies the existing webhook secret
// for this request only; the wrapper never discovers, creates, stores or logs it.
// Callbacks must be concurrency-safe and must not change authority namespaces.
// Authorize also owns retaining any required raw-body/delivery-header audit.
type GitHubWebhookConfig struct {
	Secret         func(context.Context) ([]byte, error)
	AllowedThreads []string
	Authorize      func(context.Context, GitHubWebhookEvent) (GitHubAuthorization, error)
}

type GitHubWebhook struct {
	adapter *Adapter
	config  GitHubWebhookConfig
}

type GitHubWebhookResult struct {
	Event     GitHubWebhookEvent
	Admission superagent.AdmissionReceipt
}

func NewGitHubWebhook(adapter *Adapter, config GitHubWebhookConfig) (*GitHubWebhook, error) {
	if adapter == nil || adapter.config.Protocol != GitHub || config.Secret == nil || config.Authorize == nil || len(config.AllowedThreads) > 1024 {
		return nil, errors.New("channel: invalid GitHub webhook configuration")
	}
	config.AllowedThreads = slices.Clone(config.AllowedThreads)
	for _, thread := range config.AllowedThreads {
		if !channelThread(GitHub, thread) {
			return nil, errors.New("channel: invalid GitHub webhook target")
		}
	}
	return &GitHubWebhook{adapter: adapter, config: config}, nil
}

// Admit consumes at most 1 MiB plus one byte from a POST request. The host owns
// HTTP listening/TLS, header/read deadlines, responses and raw evidence audit.
// Only exact bytes are signed; no re-encoding occurs. A successful call admits
// input through the existing durable gate, never sends a reply. ErrGitHubReplay
// returns the original admission and this occurrence's unmodified evidence.
// GitHub provides no signed timestamp/delivery ID: an old valid body never seen
// here can still pass HMAC. The host must enforce freshness if it needs it.
func (h *GitHubWebhook) Admit(r *http.Request) (result GitHubWebhookResult, err error) {
	// Host callback failures never expose secrets or partially authorize ingress.
	defer func() {
		if recover() != nil {
			err = errors.New("channel: GitHub host callback failed")
		}
	}()
	if r == nil || r.Body == nil || r.Method != http.MethodPost || r.Context().Err() != nil || r.ContentLength > 1<<20 || len(r.Header.Values("Content-Encoding")) != 0 {
		return result, errors.New("channel: invalid GitHub HTTP request")
	}
	contentType, ok := githubSingleHeader(r.Header, "Content-Type")
	if !ok {
		return result, errors.New("channel: invalid GitHub content type")
	}
	media, params, e := mime.ParseMediaType(contentType)
	if e != nil || media != "application/json" || (params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8")) {
		return result, errors.New("channel: invalid GitHub content type")
	}
	delivery, ok := githubSingleHeader(r.Header, "X-GitHub-Delivery")
	if !ok || !githubDeliveryID(delivery) {
		return result, errors.New("channel: invalid GitHub delivery header")
	}
	eventType, ok := githubSingleHeader(r.Header, "X-GitHub-Event")
	if !ok || (eventType != "issue_comment" && eventType != "pull_request_review" && eventType != "pull_request_review_comment") {
		return result, errors.New("channel: unsupported GitHub event header")
	}
	signature, ok := githubSingleHeader(r.Header, "X-Hub-Signature-256")
	if !ok || len(signature) != 71 || !strings.HasPrefix(signature, "sha256=") {
		return result, errors.New("channel: invalid GitHub signature")
	}
	signatureBytes, e := hex.DecodeString(signature[7:])
	if e != nil {
		return result, errors.New("channel: invalid GitHub signature")
	}
	body, e := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if e != nil || len(body) == 0 || len(body) > 1<<20 {
		return result, errors.New("channel: invalid GitHub body size or read")
	}
	secret, e := h.config.Secret(r.Context())
	if e != nil || len(secret) < 32 || len(secret) > 64<<10 {
		return result, errors.New("channel: GitHub signing secret unavailable")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	if !hmac.Equal(signatureBytes, mac.Sum(nil)) {
		return result, errors.New("channel: invalid GitHub signature")
	}
	root, e := ingressJSON(body)
	if e != nil {
		return result, errors.New("channel: invalid GitHub JSON")
	}
	// The event header is unsigned. Require one unambiguous supported body shape
	// before using it, rather than allowing a header to reinterpret signed data.
	_, issue := root["issue"]
	_, pull := root["pull_request"]
	_, comment := root["comment"]
	_, review := root["review"]
	if (eventType == "issue_comment" && (!issue || pull || !comment || review)) || (eventType == "pull_request_review_comment" && (issue || !pull || !comment || review)) || (eventType == "pull_request_review" && (issue || !pull || comment || !review)) {
		return result, errors.New("channel: GitHub event header/body mismatch")
	}
	numberKey, contentKey := "pull_request", "comment"
	if eventType == "issue_comment" {
		numberKey = "issue"
	}
	if eventType == "pull_request_review" {
		contentKey = "review"
	}
	repo := ingressObject(root, "repository")
	org := ingressDecimalID(ingressObject(repo, "owner")["id"])
	actor := ingressDecimalID(ingressObject(root, "sender")["id"])
	thread := ingressDecimalID(repo["id"]) + "/pull/" + ingressDecimalID(ingressObject(root, numberKey)["number"])
	message := ingressDecimalID(ingressObject(root, contentKey)["id"])
	sum := sha256.Sum256(body)
	event := GitHubWebhookEvent{Organization: org, Principal: actor, Thread: thread, MessageID: message, EventType: eventType, DeliveryID: delivery, BodySHA256: hex.EncodeToString(sum[:]), RawBody: slices.Clone(body)}
	event.Action, _ = root["action"].(string)
	identity := Identity{Organization: org, Principal: actor, Thread: thread, EventID: "github-body:" + event.BodySHA256, MessageID: message, EventType: eventType}
	if org != h.adapter.config.Organization || !slices.Contains(h.config.AllowedThreads, thread) {
		return result, errors.New("channel: GitHub target not allowed")
	}
	// Fully validate signed content before presenting it for authority decisions.
	if _, e = Normalize(GitHub, h.adapter.config.Source, identity, body); e != nil {
		return result, errors.New("channel: invalid GitHub event")
	}
	result.Event = event
	callbackEvent := event
	callbackEvent.RawBody = slices.Clone(body)
	grant, e := h.config.Authorize(r.Context(), callbackEvent)
	if e != nil || !grant.Allowed || r.Context().Err() != nil {
		return result, errors.New("channel: GitHub event not authorized")
	}
	identity.ExplicitRequest = grant.ExplicitRequest
	identity.TaskID = grant.TaskID
	identity.ToThread = grant.ToThread
	result.Admission, e = h.adapter.Admit(identity, body)
	if e != nil {
		// A prior body's frozen routing decision wins over a later host decision.
		// Read only already-journaled evidence; never retry an unrecorded event.
		// This also handles concurrent first admissions with different grants.
		for _, prior := range h.adapter.agent.Admissions() {
			if prior.Envelope.Trigger.Source == h.adapter.config.Source && prior.Envelope.Trigger.ID == identity.EventID {
				receipt, lookupErr := h.adapter.agent.Admit(h.adapter.config.Policy, prior.Envelope)
				if lookupErr == nil && receipt.Duplicate {
					result.Admission = receipt
					return result, ErrGitHubReplay
				}
				break
			}
		}
		return result, e
	}
	if result.Admission.Duplicate {
		return result, ErrGitHubReplay
	}
	return result, nil
}

func githubSingleHeader(header http.Header, name string) (string, bool) {
	values := header.Values(name)
	if len(values) != 1 || !ingressIdentity(values[0]) {
		return "", false
	}
	return values[0], true
}
func githubDeliveryID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
