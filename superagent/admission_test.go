package superagent

import (
	"context"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestAdmissionRulesAuditAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.jsonl")
	var calls atomic.Int32
	router := func(ctx context.Context, job MainBatchJob) (MainBatchOutcome, error) {
		calls.Add(1)
		return batchReply(ctx, job)
	}
	a, err := OpenMainBatch(t.Context(), path, 1, MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}, router, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	policy := AdmissionPolicy{Version: "v1", Rules: []AdmissionRule{{Source: "im", Principal: "owner", Group: "team"}}}
	input := AdmissionEnvelope{Trigger: Trigger{ID: "request", Source: "im", Thread: "chat", Kind: "request", Text: "do work"}, Principal: "owner", Group: "team", ExplicitRequest: true}
	for _, reason := range []string{"explicit_request", "policy_denied", "unassociated_signal", "noise"} {
		env := input
		env.Trigger.ID = reason
		switch reason {
		case "policy_denied":
			env.Principal = "stranger"
		case "unassociated_signal":
			env.Trigger.Kind = "signal"
			env.ExplicitRequest = false
		case "noise":
			env.Trigger.Kind = "noise"
			env.ExplicitRequest = false
		}
		got, err := a.Admit(policy, env)
		if err != nil || got.Record.Reason != reason || got.Duplicate {
			t.Fatalf("%s: %+v %v", reason, got, err)
		}
		policy.Version = "v2"
		again, err := a.Admit(policy, env)
		if err != nil || !again.Duplicate || again.Record.PolicyVersion != got.Record.PolicyVersion {
			t.Fatalf("reevaluated duplicate: %+v %v", again, err)
		}
		changed := env
		changed.Principal = "different"
		if _, err := a.Admit(policy, changed); err == nil {
			t.Fatal("duplicate changed identity")
		}
	}
	waitIdle(t, a)
	if calls.Load() != 1 || len(a.Replies()) != 1 || len(a.ReportsSince(0)) != 4 {
		t.Fatalf("filtered events called router: %d %+v", calls.Load(), a.ReportsSince(0))
	}
	before := a.Admissions()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMainBatch(t.Context(), path, 1, MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}, router, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	waitIdle(t, b)
	if !reflect.DeepEqual(before, b.Admissions()) || calls.Load() != 1 {
		t.Fatal("admission replay changed result")
	}
}

func TestAdmissionFrozenThreadTargetsAndNoEmptyFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.jsonl")
	a, err := OpenMain(t.Context(), path, 2, mainFixture, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, id := range []string{"a", "b", "c"} {
		submit(t, a, Trigger{ID: id, Source: "im", Thread: "chat", Kind: "request"})
		waitIdle(t, a)
	}
	tasks := a.Status().Tasks
	policy := AdmissionPolicy{Version: "v1", Rules: []AdmissionRule{{Source: "git", Principal: "bot", Group: "repo"}}}
	env := AdmissionEnvelope{Trigger: Trigger{ID: "empty", Source: "git", Thread: "pr-1", Kind: "signal"}, Principal: "bot", Group: "repo", ToThread: true}
	rejected, err := a.Admit(policy, env)
	if err != nil || rejected.Record.Reason != "unassociated_signal" {
		t.Fatalf("empty subscription signal fallback: %+v %v", rejected, err)
	}
	for _, task := range tasks[:2] {
		if err := a.SetThreadSubscription(task.ID, "git", "pr-1", true); err != nil {
			t.Fatal(err)
		}
	}
	env.Trigger.ID = "before-change"
	first, err := a.Admit(policy, env)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetThreadSubscription(tasks[1].ID, "git", "pr-1", false); err != nil {
		t.Fatal(err)
	}
	if err := a.SetThreadSubscription(tasks[2].ID, "git", "pr-1", true); err != nil {
		t.Fatal(err)
	}
	env.Trigger.ID = "after-change"
	second, err := a.Admit(policy, env)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Record.Targets, []string{tasks[0].ID, tasks[1].ID}) || !reflect.DeepEqual(second.Record.Targets, []string{tasks[0].ID, tasks[2].ID}) || len(first.DeliveryIDs) != 2 || first.DeliveryIDs[0] == first.DeliveryIDs[1] {
		t.Fatalf("unfrozen admission: %+v %+v", first, second)
	}
	waitIdle(t, a)
	before := a.Admissions()
	reports := a.ReportsSince(0)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMain(t.Context(), path, 2, mainFixture, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	waitIdle(t, b)
	if !reflect.DeepEqual(before, b.Admissions()) || !reflect.DeepEqual(reports, b.ReportsSince(0)) {
		t.Fatal("gate replay altered frozen deliveries")
	}
	env.Trigger.ID = "before-change"
	duplicate, err := b.Admit(AdmissionPolicy{Version: "deny-all"}, env)
	if err != nil || !duplicate.Duplicate || !reflect.DeepEqual(duplicate.Record, first.Record) {
		t.Fatalf("duplicate reevaluated current rules/subscriptions: %+v %v", duplicate, err)
	}
	copy := b.Admissions()
	copy[1].Targets[0] = "mutated"
	if b.Admissions()[1].Targets[0] == "mutated" {
		t.Fatal("audit exposes private target slice")
	}
}

func TestAdmissionTupleAndIntentBoundary(t *testing.T) {
	var calls atomic.Int32
	route := func(ctx context.Context, job MainBatchJob) (MainBatchOutcome, error) {
		calls.Add(1)
		return batchReply(ctx, job)
	}
	a, err := OpenMainBatch(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}, route, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	policy := AdmissionPolicy{Version: "v1", Rules: []AdmissionRule{{Source: "im", Principal: "owner", Group: "team"}, {Source: "other", Principal: "member", Group: "another"}}}
	base := AdmissionEnvelope{Trigger: Trigger{ID: "x", Source: "im", Thread: "chat", Kind: "request"}, Principal: "owner", Group: "team", ExplicitRequest: true}
	for _, field := range []string{"source", "principal", "group", "cross_tuple", "not_explicit"} {
		env := base
		env.Trigger.ID = field
		switch field {
		case "source":
			env.Trigger.Source = "fake"
		case "principal":
			env.Principal = "stranger"
		case "group":
			env.Group = "public"
		case "cross_tuple":
			env.Principal = "member"
		case "not_explicit":
			env.ExplicitRequest = false
		}
		result, err := a.Admit(policy, env)
		if err != nil || result.Record.Decision != "ignored" {
			t.Fatalf("%s allowed: %+v %v", field, result, err)
		}
	}
	waitIdle(t, a)
	if calls.Load() != 0 {
		t.Fatal("unauthorized ingress reached main")
	}
	bad := base
	bad.Trigger.Kind = "signal"
	if _, err := a.Admit(policy, bad); err == nil {
		t.Fatal("inconsistent intent accepted")
	}
	bad = base
	bad.TaskID = "task-unknown"
	if _, err := a.Admit(policy, bad); err == nil {
		t.Fatal("unknown host task silently accepted")
	}
}
