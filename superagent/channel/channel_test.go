package channel_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/superagent"
	"github.com/Icatme/pi-go/superagent/channel"
)

// protocolSimulator roundtrips actual request/receipt JSON without a socket,
// credential or live platform. Its registry intentionally does not survive
// replacement; a missing operation is unknown, never proof of no delivery.
type protocolSimulator struct {
	mu       sync.Mutex
	mode     string
	requests []channel.Request
	receipts map[superagent.ReplyDelivery]channel.Receipt
	retryAt  int64
	messages int
}

func newSimulator(mode string) *protocolSimulator {
	return &protocolSimulator{mode: mode, receipts: make(map[superagent.ReplyDelivery]channel.Receipt)}
}
func wire[T any](v T) T {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	var out T
	if e = json.Unmarshal(b, &out); e != nil {
		panic(e)
	}
	return out
}
func matching(r channel.Request, status string) channel.Receipt {
	return channel.Receipt{Source: r.Source, Organization: r.Organization, Thread: r.Thread, Delivery: r.Delivery, Status: status}
}
func (s *protocolSimulator) Send(_ context.Context, r channel.Request) (channel.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r = wire(r)
	s.requests = append(s.requests, r)
	var body map[string]string
	if e := json.Unmarshal(r.Body, &body); e != nil {
		return channel.Receipt{}, e
	}
	if (r.Protocol == channel.IM && (len(body) != 2 || body["thread_id"] != r.Thread)) || (r.Protocol == channel.GitHub && len(body) != 1) || r.IdempotencyKey != r.Delivery.ReplyID {
		return channel.Receipt{}, errors.New("bad wire request")
	}
	receipt := matching(r, "delivered")
	switch s.mode {
	case "panic":
		panic("PRIVATE transport error")
	case "error":
		return channel.Receipt{}, errors.New("PRIVATE transport error")
	case "wrong_thread":
		receipt.Thread = "other"
		return wire(receipt), nil
	case "rate":
		receipt.Status = "rate_limited"
		receipt.RetryAt = s.retryAt
	case "not_delivered":
		receipt.Status = "not_delivered"
	default:
		s.messages++
		receipt.MessageID = fmt.Sprint(900 + s.messages)
		receipt.Cursor = fmt.Sprint(s.messages)
	}
	s.receipts[r.Delivery] = receipt
	if s.mode == "lost_ack" {
		return channel.Receipt{}, errors.New("PRIVATE disconnected after commit")
	}
	return wire(receipt), nil
}
func (s *protocolSimulator) Lookup(_ context.Context, r channel.Request) (channel.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r = wire(r)
	if v, ok := s.receipts[r.Delivery]; ok {
		return wire(v), nil
	}
	return matching(r, "unknown"), nil
}
func (s *protocolSimulator) setMode(mode string) { s.mu.Lock(); defer s.mu.Unlock(); s.mode = mode }
func (s *protocolSimulator) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests), s.messages
}
func (s *protocolSimulator) last() channel.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return wire(s.requests[len(s.requests)-1])
}

func config(protocol channel.Protocol) channel.Config {
	return channel.Config{Protocol: protocol, Source: string(protocol) + ":42", Organization: "42", Policy: superagent.AdmissionPolicy{Version: "v1", Rules: []superagent.AdmissionRule{{Source: string(protocol) + ":42", Principal: "7", Group: "42"}}}}
}
func host(t *testing.T, path string, worker superagent.Worker) *superagent.SuperAgent {
	t.Helper()
	route := func(_ context.Context, j superagent.MainJob) (superagent.MainOutcome, error) {
		d := superagent.MainDecision{Action: "reply", Text: "Approved only: hello <world>\n第二行"}
		if worker != nil {
			d = superagent.MainDecision{Action: "new"}
			if j.Input.Kind == "worker_result" {
				d = superagent.MainDecision{Action: "reply", Text: "Approved: " + j.Input.Report.Text}
			} else if j.Input.Kind == "task_input" {
				d = superagent.MainDecision{Action: "ignore"}
			}
		}
		return superagent.MainOutcome{Decision: d, Snapshot: j.Snapshot}, nil
	}
	work := worker
	if work == nil {
		work = func(context.Context, superagent.Job) (superagent.Outcome, error) {
			t.Error("unexpected work")
			return superagent.Outcome{}, nil
		}
	}
	a, e := superagent.OpenMain(t.Context(), path, 2, route, work)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close() })
	return a
}
func adapter(t *testing.T, a *superagent.SuperAgent, p channel.Protocol, s channel.Transport) *channel.Adapter {
	t.Helper()
	v, e := channel.New(a, config(p), s)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func inbound(protocol channel.Protocol, event, text string) (channel.Identity, []byte) {
	id := channel.Identity{Organization: "42", Principal: "7", Thread: "chat", EventID: event, ExplicitRequest: true}
	var payload any
	if protocol == channel.IM {
		payload = map[string]any{"organization_id": "42", "actor_id": "7", "thread_id": "chat", "message_id": event, "text": text}
	} else {
		id.Thread = "100/pull/27"
		id.EventType = "issue_comment"
		id.MessageID = "81"
		payload = map[string]any{"action": "created", "sender": map[string]any{"id": 7}, "repository": map[string]any{"id": 100, "owner": map[string]any{"id": 42, "type": "User"}}, "issue": map[string]any{"number": 27, "pull_request": map[string]any{}}, "comment": map[string]any{"id": 81, "user": map[string]any{"id": 7}, "body": text}}
	}
	b, e := json.Marshal(payload)
	if e != nil {
		panic(e)
	}
	return id, b
}
func admit(t *testing.T, a *superagent.SuperAgent, adapter *channel.Adapter, p channel.Protocol, id string) superagent.Reply {
	t.Helper()
	identity, payload := inbound(p, id, "request")
	if r, e := adapter.Admit(identity, payload); e != nil || r.Record.Decision != "accepted" {
		t.Fatalf("admit %+v %v", r, e)
	}
	if e := a.WaitIdle(t.Context()); e != nil {
		t.Fatal(e)
	}
	replies := a.Replies()
	return replies[len(replies)-1]
}
func TestProtocolContract(t *testing.T) {
	for _, p := range []channel.Protocol{channel.IM, channel.GitHub} {
		t.Run(string(p), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "main.jsonl")
			a := host(t, path, nil)
			sim := newSimulator("")
			c := adapter(t, a, p, sim)
			reply := admit(t, a, c, p, "event-1")
			identity, payload := inbound(p, "event-1", "request")
			if r, e := c.Admit(identity, payload); e != nil || !r.Duplicate {
				t.Fatalf("dedupe: %+v %v", r, e)
			}
			identity, payload = inbound(p, "event-1", "changed")
			if _, e := c.Admit(identity, payload); e == nil {
				t.Fatal("changed callback accepted")
			}
			d, e := c.SendReply(t.Context(), reply.ID)
			if e != nil {
				t.Fatal(e)
			}
			request := sim.last()
			var content map[string]string
			json.Unmarshal(request.Body, &content)
			body := content["text"]
			if p == channel.GitHub {
				body = content["body"]
			}
			if body != reply.Text || request.Thread != reply.Thread || request.Source != reply.Source || d.Attempt != 1 {
				t.Fatalf("wrong content/destination: %+v", request)
			}
			got := a.Replies()[0]
			messageID := "901"
			if p == channel.GitHub {
				messageID = "issue_comment:901"
			}
			if got.Status != "acknowledged" || got.Attempts[0].Cursor != "1" || got.Attempts[0].MessageID != messageID {
				t.Fatalf("receipt: %+v", got)
			}
			if _, e := c.SendReply(t.Context(), reply.ID); e == nil {
				t.Fatal("sent acknowledged reply")
			}
			if e := c.Observe(matching(request, "delivered")); e == nil {
				t.Fatal("accepted missing message identity")
			}
			if got := a.Replies()[0]; got.Status != "acknowledged" {
				t.Fatal("invalid receipt erased delivered fact")
			}
			if e := a.Close(); e != nil {
				t.Fatal(e)
			}
			a = host(t, path, nil)
			c = adapter(t, a, p, sim)
			if r, e := c.Admit(func() channel.Identity { i, _ := inbound(p, "event-1", "request"); return i }(), func() []byte { _, b := inbound(p, "event-1", "request"); return b }()); e != nil || !r.Duplicate {
				t.Fatal("restart dedupe lost")
			}
			if got := a.Replies()[0]; got.Attempts[0].Cursor != "1" || got.Status != "acknowledged" {
				t.Fatal("restart receipt lost")
			}
			// Authenticate and codec-check a known own-message identity. GitHub's
			// webhook callback is different from the comment's platform message ID.
			echoID, echo := inbound(p, "901", reply.Text)
			if p == channel.GitHub {
				echoID.EventID = "echo-delivery"
				echoID.MessageID = "901"
				echo = []byte(strings.Replace(string(echo), `"id":81`, `"id":901`, 1))
			}
			if r, e := c.Admit(echoID, echo); e != nil || !r.Duplicate {
				t.Fatalf("echo: %+v %v", r, e)
			}
			if e := a.WaitIdle(t.Context()); e != nil {
				t.Fatal(e)
			}
			if len(a.Replies()) != 1 {
				t.Fatal("echo reached router")
			}
		})
	}
}
func TestUnknownOutcomesAndRecovery(t *testing.T) {
	for _, p := range []channel.Protocol{channel.IM, channel.GitHub} {
		for _, mode := range []string{"lost_ack", "error", "panic", "wrong_thread"} {
			t.Run(string(p)+"/"+mode, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "main.jsonl")
				a := host(t, path, nil)
				sim := newSimulator(mode)
				c := adapter(t, a, p, sim)
				r := admit(t, a, c, p, "event")
				d, e := c.SendReply(t.Context(), r.ID)
				if !errors.Is(e, channel.ErrUncertain) || strings.Contains(e.Error(), "PRIVATE") {
					t.Fatalf("unsafe error: %v", e)
				}
				if _, e := c.SendReply(t.Context(), r.ID); e == nil {
					t.Fatal("unknown resent")
				}
				if a.Replies()[0].Status != "needs_review" {
					t.Fatal("unknown not fenced")
				}
				a.Close()
				a = host(t, path, nil)
				c = adapter(t, a, p, sim)
				if _, e := c.SendReply(t.Context(), r.ID); e == nil {
					t.Fatal("restart resent")
				}
				// Replacing the simulator loses evidence and must not create a retry.
				fresh := adapter(t, a, p, newSimulator(""))
				if e := fresh.Reconcile(t.Context(), d); !errors.Is(e, channel.ErrUncertain) {
					t.Fatalf("missing lookup proof: %v", e)
				}
				if mode == "lost_ack" {
					if e := c.Reconcile(t.Context(), d); e != nil {
						t.Fatal(e)
					}
					if a.Replies()[0].Status != "acknowledged" {
						t.Fatal("lookup lost commit")
					}
				}
				calls, _ := sim.counts()
				if calls != 1 {
					t.Fatal("lookup resent")
				}
			})
		}
	}
}
func TestRateLimitIsDurableAndSourceWide(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.jsonl")
	a := host(t, path, nil)
	sim := newSimulator("rate")
	sim.retryAt = time.Now().Add(time.Second).UnixMilli()
	c := adapter(t, a, channel.IM, sim)
	first := admit(t, a, c, channel.IM, "first")
	second := admit(t, a, c, channel.IM, "second")
	d, e := c.SendReply(t.Context(), first.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{first.ID, second.ID} {
		if _, e := c.SendReply(t.Context(), id); !errors.Is(e, superagent.ErrReplyRateLimited) {
			t.Fatalf("rate limit: %v", e)
		}
	}
	a.Close()
	a = host(t, path, nil)
	c = adapter(t, a, channel.IM, sim)
	if _, e := c.SendReply(t.Context(), second.ID); !errors.Is(e, superagent.ErrReplyRateLimited) {
		t.Fatalf("restart deadline: %v", e)
	}
	if r := a.Replies()[0]; r.Attempts[0].RetryAt != sim.retryAt || r.Attempts[0].Status != "not_delivered" {
		t.Fatal("retry facts lost")
	}
	gh := adapter(t, a, channel.GitHub, newSimulator(""))
	other := admit(t, a, gh, channel.GitHub, "other")
	if _, e := gh.SendReply(t.Context(), other.ID); e != nil {
		t.Fatalf("other source throttled: %v", e)
	}
	time.Sleep(time.Until(time.UnixMilli(sim.retryAt)) + time.Millisecond)
	sim.setMode("")
	retry, e := c.SendReply(t.Context(), first.ID)
	if e != nil || retry.Attempt != 2 || retry.ReplyID != d.ReplyID {
		t.Fatalf("safe retry: %+v %v", retry, e)
	}
	if sim.last().IdempotencyKey != first.ID {
		t.Fatal("retry changed stable identity")
	}
	// A stale rate response must not overwrite the newer delivered attempt.
	stale := matching(sim.last(), "rate_limited")
	stale.Delivery = d
	stale.RetryAt = sim.retryAt
	if e := c.Observe(stale); e != nil {
		t.Fatal(e)
	}
	if a.Replies()[0].Status != "acknowledged" {
		t.Fatal("stale receipt reset newer send")
	}
}
func TestConcurrentSendHasOneClaim(t *testing.T) {
	a := host(t, filepath.Join(t.TempDir(), "main.jsonl"), nil)
	sim := newSimulator("")
	c := adapter(t, a, channel.IM, sim)
	r := admit(t, a, c, channel.IM, "event")
	var winners atomic.Int32
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() {
			if _, e := c.SendReply(t.Context(), r.ID); e == nil {
				winners.Add(1)
			}
		})
	}
	wg.Wait()
	calls, messages := sim.counts()
	if winners.Load() != 1 || calls != 1 || messages != 1 {
		t.Fatalf("multiple sends %d %d %d", winners.Load(), calls, messages)
	}
}
func TestCrossSourceTaskHistoryAndReopen(t *testing.T) {
	var seenMu sync.Mutex
	var seen []superagent.Job
	work := func(_ context.Context, j superagent.Job) (superagent.Outcome, error) {
		seenMu.Lock()
		seen = append(seen, j)
		seenMu.Unlock()
		snap := j.Snapshot
		snap.SessionID = j.TaskID
		snap.Messages = append(snap.Messages, agent.Message{Role: agent.RoleUser, Parts: []agent.Part{{Type: "text", Text: j.Trigger.Text}}})
		return superagent.Outcome{Summary: j.Trigger.Text, Snapshot: snap}, nil
	}
	path := filepath.Join(t.TempDir(), "main.jsonl")
	a := host(t, path, work)
	imSim, ghSim := newSimulator(""), newSimulator("")
	im := adapter(t, a, channel.IM, imSim)
	original := admit(t, a, im, channel.IM, "request")
	if _, e := im.SendReply(t.Context(), original.ID); e != nil {
		t.Fatal(e)
	}
	a.Close()
	a = host(t, path, work)
	gh := adapter(t, a, channel.GitHub, ghSim)
	id, body := inbound(channel.GitHub, "review-webhook", "Only the last hour")
	id.ExplicitRequest = false
	id.TaskID = original.TaskID
	receipt, e := gh.Admit(id, body)
	if e != nil {
		t.Fatal(e)
	}
	duplicate, e := gh.Admit(id, body)
	if e != nil || !duplicate.Duplicate || len(receipt.DeliveryIDs) != 1 {
		t.Fatal("review dedupe")
	}
	if e := a.WaitIdle(t.Context()); e != nil {
		t.Fatal(e)
	}
	replies := a.Replies()
	if len(replies) != 2 || replies[1].TaskID != original.TaskID || replies[1].Source != config(channel.GitHub).Source || replies[1].Thread != id.Thread {
		t.Fatalf("reply association: %+v", replies)
	}
	if _, e := gh.SendReply(t.Context(), replies[1].ID); e != nil {
		t.Fatal(e)
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	if len(seen) != 2 || seen[0].TaskID != seen[1].TaskID || seen[0].ProviderSessionID != seen[1].ProviderSessionID || len(seen[1].Snapshot.Messages) != 1 {
		t.Fatalf("history lost: %+v", seen)
	}
	if imSim.last().Thread != "chat" || ghSim.last().Thread != "100/pull/27" {
		t.Fatal("cross-source destination changed")
	}
}
func TestHostBoundaryAndCancellation(t *testing.T) {
	a := host(t, filepath.Join(t.TempDir(), "main.jsonl"), nil)
	sim := newSimulator("")
	cfg := config(channel.IM)
	c, e := channel.New(a, cfg, sim)
	if e != nil {
		t.Fatal(e)
	}
	cfg.Policy.Rules[0].Principal = "attacker" // configuration must be frozen
	r := admit(t, a, c, channel.IM, "event")
	wrong := adapter(t, a, channel.GitHub, sim)
	if _, e := wrong.SendReply(t.Context(), r.ID); e == nil {
		t.Fatal("wrong source sent")
	}
	if _, e := c.SendReply(t.Context(), "arbitrary-text"); e == nil {
		t.Fatal("sent non-approved reply")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e := c.SendReply(ctx, r.ID); e == nil {
		t.Fatal("canceled claim")
	}
	if len(a.Replies()[0].Attempts) != 0 {
		t.Fatal("pre-dispatch cancellation claimed")
	}
	id, b := inbound(channel.IM, "bad", "request")
	id.Organization = "other"
	if _, e := c.Admit(id, b); e == nil {
		t.Fatal("cross organization accepted")
	}
	if calls, _ := sim.counts(); calls != 0 {
		t.Fatal("boundary violation sent")
	}
}

func TestGitHubObjectNamespacesNeverHideReview(t *testing.T) {
	a := host(t, filepath.Join(t.TempDir(), "main.jsonl"), nil)
	sim := newSimulator("")
	c := adapter(t, a, channel.GitHub, sim)
	reply := admit(t, a, c, channel.GitHub, "initial")
	if _, e := c.SendReply(t.Context(), reply.ID); e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"pull_request_review", "pull_request_review_comment"} {
		id := channel.Identity{Organization: "42", Principal: "7", Thread: "100/pull/27", EventID: kind + "-delivery", EventType: kind, MessageID: "901", ExplicitRequest: true}
		field, action := "comment", "created"
		if kind == "pull_request_review" {
			field, action = "review", "submitted"
		}
		payload := []byte(fmt.Sprintf(`{"action":%q,"sender":{"id":7},"repository":{"id":100,"owner":{"id":42}},"pull_request":{"number":27},%q:{"id":901,"user":{"id":7},"body":"New review"}}`, action, field))
		got, e := c.Admit(id, payload)
		if e != nil || got.Duplicate || got.Record.Decision != "accepted" || got.Record.Envelope.MessageID != kind+":901" {
			t.Fatalf("cross-kind ID collision: %+v %v", got, e)
		}
	}
	if e := a.WaitIdle(t.Context()); e != nil {
		t.Fatal(e)
	}
	if len(a.Replies()) != 3 {
		t.Fatal("legitimate review suppressed")
	}
}

func TestAtomicPlatformEchoAdmission(t *testing.T) {
	// Exercise the core's final admission boundary directly, without any adapter
	// snapshot. Exactly one of prior admission or delivery confirmation may win.
	for range 20 {
		a := host(t, filepath.Join(t.TempDir(), "main.jsonl"), nil)
		c := adapter(t, a, channel.GitHub, newSimulator(""))
		r := admit(t, a, c, channel.GitHub, "initial")
		d, e := a.BeginReplyDelivery(r.ID)
		if e != nil {
			t.Fatal(e)
		}
		id, body := inbound(channel.GitHub, "different-webhook", "echo")
		id.MessageID = "901"
		body = []byte(strings.Replace(string(body), `"id":81`, `"id":901`, 1))
		envelope, e := channel.Normalize(channel.GitHub, config(channel.GitHub).Source, id, body)
		if e != nil {
			t.Fatal(e)
		}
		var admission superagent.AdmissionReceipt
		var admitErr, observeErr error
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Go(func() { <-start; admission, admitErr = a.Admit(config(channel.GitHub).Policy, envelope) })
		wg.Go(func() { <-start; observeErr = a.ObserveReply(r.Source, r.Thread, d, "issue_comment:901") })
		close(start)
		wg.Wait()
		if admitErr != nil {
			t.Fatal(admitErr)
		}
		if observeErr == nil && !admission.Duplicate {
			t.Fatal("delivery and business admission both won")
		}
		if observeErr != nil && admission.Duplicate {
			t.Fatal("neither identity won")
		}
		a.Close()
	}
}

func TestConfigurationCannotRebindPersistedSource(t *testing.T) {
	a := host(t, filepath.Join(t.TempDir(), "main.jsonl"), nil)
	cfg := config(channel.IM)
	cfg.Organization = "43"
	if _, e := channel.New(a, cfg, newSimulator("")); e == nil {
		t.Fatal("same source rebound to another account")
	}
	cfg = config(channel.IM)
	cfg.Protocol = channel.GitHub
	if _, e := channel.New(a, cfg, newSimulator("")); e == nil {
		t.Fatal("same source rebound to another protocol")
	}
}

type callbackTransport struct {
	send func(channel.Request) (channel.Receipt, error)
}

func (t callbackTransport) Send(_ context.Context, r channel.Request) (channel.Receipt, error) {
	return t.send(r)
}
func (callbackTransport) Lookup(_ context.Context, r channel.Request) (channel.Receipt, error) {
	return matching(r, "unknown"), nil
}

func TestEarlyReceiptSurvivesLateTransportFailure(t *testing.T) {
	a := host(t, filepath.Join(t.TempDir(), "main.jsonl"), nil)
	var c *channel.Adapter
	transport := callbackTransport{send: func(r channel.Request) (channel.Receipt, error) {
		receipt := matching(r, "delivered")
		receipt.MessageID = "901"
		receipt.Cursor = "1"
		if e := c.Observe(receipt); e != nil {
			t.Fatal(e)
		}
		return channel.Receipt{}, errors.New("PRIVATE lost final response")
	}}
	c = adapter(t, a, channel.GitHub, transport)
	r := admit(t, a, c, channel.GitHub, "event")
	if _, e := c.SendReply(t.Context(), r.ID); !errors.Is(e, channel.ErrUncertain) {
		t.Fatalf("late error not reported: %v", e)
	}
	got := a.Replies()[0]
	if got.Status != "acknowledged" || got.Attempts[0].MessageID != "issue_comment:901" || got.Attempts[0].Cursor != "1" {
		t.Fatalf("late failure erased fact: %+v", got)
	}
	if _, e := c.SendReply(t.Context(), r.ID); e == nil {
		t.Fatal("late failure allowed duplicate send")
	}
}

func TestPolicyDenialAndObservationDestination(t *testing.T) {
	a := host(t, filepath.Join(t.TempDir(), "main.jsonl"), nil)
	c := adapter(t, a, channel.IM, newSimulator(""))
	id, b := inbound(channel.IM, "denied", "request")
	id.Principal = "8"
	b = []byte(strings.Replace(string(b), `"actor_id":"7"`, `"actor_id":"8"`, 1))
	got, e := c.Admit(id, b)
	if e != nil || got.Record.Decision != "ignored" || got.Record.Reason != "policy_denied" {
		t.Fatalf("principal policy bypass: %+v %v", got, e)
	}
	if e := a.WaitIdle(t.Context()); e != nil {
		t.Fatal(e)
	}
	if len(a.Replies()) != 0 {
		t.Fatal("denied principal reached router")
	}
	r := admit(t, a, c, channel.IM, "allowed")
	d, e := a.BeginReplyDelivery(r.ID)
	if e != nil {
		t.Fatal(e)
	}
	bad := channel.Receipt{Source: r.Source, Organization: "other", Thread: r.Thread, Delivery: d, Status: "delivered", MessageID: "m"}
	if e := c.Observe(bad); e == nil {
		t.Fatal("foreign receipt accepted")
	}
	if got := a.Replies()[0]; got.Attempts[0].Status != "started" {
		t.Fatal("foreign callback changed original attempt")
	}
}
