package channel_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/superagent"
	"github.com/Icatme/pi-go/superagent/channel"
)

// This transport is only an in-memory protocol simulator. A real host must
// implement authenticated sends and authoritative read-only reconciliation.
type exampleTransport struct{}

func (exampleTransport) Send(_ context.Context, r channel.Request) (channel.Receipt, error) {
	var body struct {
		Text string `json:"text"`
	}
	if e := json.Unmarshal(r.Body, &body); e != nil {
		return channel.Receipt{}, e
	}
	fmt.Println(r.Protocol, r.Thread, body.Text)
	return channel.Receipt{Source: r.Source, Organization: r.Organization, Thread: r.Thread, Delivery: r.Delivery, Status: "delivered", MessageID: "sim-message-1", Cursor: "page-1"}, nil
}
func (exampleTransport) Lookup(_ context.Context, r channel.Request) (channel.Receipt, error) {
	return channel.Receipt{Source: r.Source, Organization: r.Organization, Thread: r.Thread, Delivery: r.Delivery, Status: "unknown"}, nil
}

func ExampleNew() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "superagent-channel-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	router := func(_ context.Context, j superagent.MainJob) (superagent.MainOutcome, error) {
		return superagent.MainOutcome{Decision: superagent.MainDecision{Action: "clarify", Text: "Which build?"}, Snapshot: j.Snapshot}, nil
	}
	worker := func(_ context.Context, j superagent.Job) (superagent.Outcome, error) {
		return superagent.Outcome{Snapshot: agent.AgentSnapshot{SessionID: j.TaskID}}, nil
	}
	coordinator, err := superagent.OpenMain(ctx, filepath.Join(dir, "main.jsonl"), 2, router, worker)
	if err != nil {
		panic(err)
	}
	defer coordinator.Close()
	config := channel.Config{Protocol: channel.IM, Source: "im:workspace-1", Organization: "workspace-1", Policy: superagent.AdmissionPolicy{Version: "v1", Rules: []superagent.AdmissionRule{{Source: "im:workspace-1", Principal: "user-7", Group: "workspace-1"}}}}
	adapter, err := channel.New(coordinator, config, exampleTransport{})
	if err != nil {
		panic(err)
	}
	// Supply these facts from host authentication/authorization, never by copying
	// unchecked JSON claims. No token or platform account is involved here.
	identity := channel.Identity{Organization: "workspace-1", Principal: "user-7", Thread: "build-chat", EventID: "message-1", ExplicitRequest: true}
	payload := []byte(`{"organization_id":"workspace-1","actor_id":"user-7","thread_id":"build-chat","message_id":"message-1","text":"Check the build"}`)
	if _, err = adapter.Admit(identity, payload); err != nil {
		panic(err)
	}
	// Only a demonstration barrier: production hosts can continue admission while
	// they independently consume the durable outbox.
	if err = coordinator.WaitIdle(ctx); err != nil {
		panic(err)
	}
	reply := coordinator.Replies()[0]
	if _, err = adapter.SendReply(ctx, reply.ID); err != nil {
		panic(err)
	}
	persisted := coordinator.Replies()[0]
	fmt.Println(persisted.Status, persisted.Attempts[0].Cursor)
	// Output:
	// im build-chat Which build?
	// acknowledged page-1
}
