package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/superagent"
)

func cliReplies(t *testing.T, data []byte) []superagent.Reply {
	t.Helper()
	var replies []superagent.Reply
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var header struct{ Type string }
		if err := json.Unmarshal(line, &header); err != nil {
			t.Fatal(err)
		}
		if header.Type != "REPLY" {
			continue
		}
		var reply superagent.Reply
		if err := json.Unmarshal(line, &reply); err != nil {
			t.Fatal(err)
		}
		replies = append(replies, reply)
	}
	return replies
}

func TestCommandMainSessionSameChatReplyRecoveryAndAcknowledgement(t *testing.T) {
	state := filepath.Join(t.TempDir(), "main.jsonl")
	input := `{"id":"a","source":"console","thread":"same","kind":"request","text":"alpha"}
{"id":"b","source":"console","thread":"same","kind":"request","text":"beta"}`
	run := func(input string) []byte {
		t.Helper()
		var out bytes.Buffer
		if err := command(t.Context(), []string{"run", "-state", state}, strings.NewReader(input), &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	first := cliReplies(t, run(input))
	if len(first) != 2 {
		t.Fatalf("want two main replies, got %+v", first)
	}
	if first[0].ID == "" || first[0].ID == first[1].ID || first[0].TaskID == first[1].TaskID {
		t.Fatalf("collapsed main identities: %+v", first)
	}
	for _, r := range first {
		if r.Source != "console" || r.Thread != "same" || r.Status != "pending" || len(r.Attempts) != 0 || r.Report == nil || r.Report.DeliveryID != r.DeliveryID {
			t.Fatalf("invalid reply: %+v", r)
		}
	}
	if replay := cliReplies(t, run(input)); !reflect.DeepEqual(first, replay) {
		t.Fatalf("reply replay changed: %v -> %v", first, replay)
	}
	ack := `{"op":"ack_reply","reply_id":"` + first[0].ID + `","request_id":"read-a"}`
	if out := run(ack + "\n" + ack); bytes.Count(out, []byte(`"type":"REPLY_ACKNOWLEDGED"`)) != 2 {
		t.Fatalf("missing idempotent consumption receipt: %s", out)
	}
	after := cliReplies(t, run(""))
	if len(after) != 2 || after[0].Status != "acknowledged" || after[1].Status != "pending" || len(after[0].Attempts) != 0 {
		t.Fatalf("ack changed delivery facts: %+v", after)
	}
	// An explicit offline follow-up selects the second task, never the shared chat.
	out := run(`{"id":"follow","source":"console","thread":"same","kind":"signal","text":"@` + first[1].TaskID + ` next"}`)
	replies := cliReplies(t, out)
	if len(replies) != 3 || replies[2].TaskID != first[1].TaskID || replies[2].EventID != "follow" {
		t.Fatalf("follow-up routed incorrectly: %s", out)
	}
}

func TestMainReplyOutputPartialFailurePreservesFactsAndPendingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	route := func(_ context.Context, j superagent.MainJob) (superagent.MainOutcome, error) {
		d := superagent.MainDecision{Action: "new"}
		if j.Input.Report != nil {
			d = superagent.MainDecision{Action: "reply", Text: "Review the attached facts."}
			for _, ref := range j.Input.HistoryRefs {
				if ref.Kind == "report" {
					d.ReportRefs = append(d.ReportRefs, ref)
				}
			}
		}
		return superagent.MainOutcome{Decision: d, Snapshot: j.Snapshot}, nil
	}
	work := func(_ context.Context, j superagent.Job) (superagent.Outcome, error) {
		return superagent.Outcome{Summary: "public summary", NeedsReview: true,
			Snapshot: agent.AgentSnapshot{SessionID: j.TaskID, Messages: []agent.Message{agent.NewUserTextMessage("PRIVATE_TRANSCRIPT")}},
			Receipts: []superagent.ToolReceipt{{Token: 1, Domain: "agent", CallID: "write", ToolName: "write", Committed: true}}}, errors.New("PRIVATE_FAILURE")
	}
	s, err := superagent.OpenMain(t.Context(), path, 1, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, id := range []string{"a", "b"} {
		if _, err := s.Submit(superagent.Trigger{ID: id, Source: "console", Thread: "same", Kind: "request", Text: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.WaitIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
	var partial bytes.Buffer
	session := &outputSession{s: s, cursor: len(s.ReportsSince(0)), out: &failingReportEncoder{out: json.NewEncoder(&partial), remaining: 1}}
	if err := session.flush(); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("flush=%v", err)
	}
	var retry bytes.Buffer
	session.out = json.NewEncoder(&retry)
	if err := session.flush(); err != nil {
		t.Fatal(err)
	}
	before, after := cliReplies(t, partial.Bytes()), cliReplies(t, retry.Bytes())
	if len(before) != 1 || len(after) != 2 || !reflect.DeepEqual(before[0], after[0]) {
		t.Fatalf("partial prefix lost or changed: %+v %+v", before, after)
	}
	for _, r := range after {
		if r.Status != "pending" || len(r.Attempts) != 0 || r.Report == nil || !r.Report.NeedsReview || !r.Report.Receipts[0].Committed || len(r.Reports) != 1 || !reflect.DeepEqual(*r.Report, r.Reports[0].Report) {
			t.Fatalf("facts lost: %+v", r)
		}
	}
	if strings.Contains(partial.String()+retry.String(), "PRIVATE_") {
		t.Fatal("private worker data leaked")
	}
	if !reflect.DeepEqual(s.Replies(), after) {
		t.Fatal("stdout changed durable reply state")
	}
}

func TestMainReplyAcknowledgementCannotResolveExternalAttempt(t *testing.T) {
	s, err := superagent.OpenMain(t.Context(), filepath.Join(t.TempDir(), "journal"), 1,
		func(_ context.Context, j superagent.MainJob) (superagent.MainOutcome, error) {
			return superagent.MainOutcome{Decision: superagent.MainDecision{Action: "reply", Text: "hello"}, Snapshot: j.Snapshot}, nil
		},
		func(context.Context, superagent.Job) (superagent.Outcome, error) {
			t.Fatal("unexpected worker")
			return superagent.Outcome{}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Submit(superagent.Trigger{ID: "a", Source: "console", Thread: "same", Kind: "request", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
	r := s.Replies()[0]
	if _, err := s.BeginReplyDelivery(r.ID); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	input := `{"op":"ack_reply","reply_id":"` + r.ID + `"}`
	if err := (&outputSession{s: s, out: json.NewEncoder(&out)}).run(t.Context(), strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "direct acknowledgement cannot resolve an external send") || s.Replies()[0].Status != "needs_review" {
		t.Fatalf("external attempt incorrectly acknowledged: %s", out.String())
	}
}

func TestCommandMainLegacyUpgradePreservesWorkerAndReports(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	def := agent.AgentDefinition{Model: superagent.OfflineModel()}
	router, err := superagent.PiRouter(def)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := superagent.PiWorker(def)
	if err != nil {
		t.Fatal(err)
	}
	s, err := superagent.Open(t.Context(), path, 1, router, worker)
	if err != nil {
		t.Fatal(err)
	}
	event := superagent.Trigger{ID: "old", Source: "console", Thread: "same", Kind: "request", Text: "old work"}
	if _, err := s.Submit(event); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
	reports := s.ReportsSince(0)
	snapshot, _, err := s.TaskSnapshot(reports[0].TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var input, out bytes.Buffer
	if err := json.NewEncoder(&input).Encode(event); err != nil {
		t.Fatal(err)
	}
	if err := command(t.Context(), []string{"run", "-state", path}, &input, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(cliReplies(t, out.Bytes())) != 0 {
		t.Fatal("old result became fresh main work")
	}
	var replay []superagent.Report
	for _, f := range decodeFrames(t, &out) {
		if f.Type == "REPORT" {
			replay = append(replay, f.Report)
		}
	}
	if !reflect.DeepEqual(reports, replay) {
		t.Fatal("legacy report identity changed")
	}
	mainRouter, err := superagent.PiMainRouter(def, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	s, err = superagent.OpenMain(t.Context(), path, 1, mainRouter, worker)
	if err != nil {
		t.Fatal(err)
	}
	after, _, err := s.TaskSnapshot(reports[0].TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot, after) {
		t.Fatal("legacy worker/provider snapshot changed")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if legacy, err := superagent.Open(t.Context(), path, 1, router, worker); err == nil {
		legacy.Close()
		t.Fatal("upgraded journal allowed downgrade")
	}
}

func TestCommandMainContextFailureIsPublicAndReplayed(t *testing.T) {
	state := filepath.Join(t.TempDir(), "main.jsonl")
	var first, replay bytes.Buffer
	input := `{"id":"private-input","source":"console","thread":"same","kind":"request","text":"PRIVATE_INPUT"}`
	if err := command(t.Context(), []string{"run", "-state", state, "-main-context-bytes", "1024"}, strings.NewReader(input), &first, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.String(), `"type":"MAIN_ERROR"`) || strings.Contains(first.String(), "PRIVATE_INPUT") || len(cliReplies(t, first.Bytes())) != 0 {
		t.Fatalf("missing safe main failure: %s", first.String())
	}
	if err := command(t.Context(), []string{"run", "-state", state}, strings.NewReader(""), &replay, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(replay.String(), `"type":"MAIN_ERROR"`) || len(cliReplies(t, replay.Bytes())) != 0 {
		t.Fatalf("failure silently retried: %s", replay.String())
	}
}

func TestReplyAcknowledgementStrictControlFields(t *testing.T) {
	for _, raw := range []string{
		`{"op":"ack_reply"}`, `{"op":"ack_reply","reply_id":""}`,
		`{"op":"ack_reply","reply_id":2}`, `{"op":"ack_reply","reply_id":"a","reply_id":"b"}`,
		`{"op":"ack_reply","reply_id":"a","message_id":"pretend-delivered"}`,
		`{"op":"ack_reply","reply_id":"a","source":"other"}`,
		`{"op":"ack_reply","reply_id":"` + strings.Repeat("x", 257) + `"}`,
	} {
		if cmd, err := parseControl([]byte(raw), 1); cmd == nil || err == nil {
			t.Fatalf("accepted invalid acknowledgement: %s", raw)
		}
	}
}

func TestMainOutputBatchSpoolRoundTrip(t *testing.T) {
	report := superagent.Report{DeliveryID: "delivery-a", Source: "console", Thread: "same", EventID: "a", TaskID: "task-a", Status: "failed", NeedsReview: true}
	reply := superagent.Reply{ID: "reply-a", DeliveryID: report.DeliveryID, Source: report.Source, Thread: report.Thread, Status: "pending", Report: &report,
		Reports: []superagent.ReportCitation{{Ref: superagent.HistoryRef{Domain: "main-a", Kind: "report", ID: report.DeliveryID}, Report: report}}}
	batch := outputBatch{reports: []superagent.Report{report}, replies: []superagent.Reply{reply}, failures: []superagent.MainFailure{{InputID: "input-b", Text: "Main router failed or returned an invalid decision."}}}
	q := &frameQueue{ready: make(chan any)} // Force the entire mixed batch to disk.
	if err := q.Encode(batch); err != nil {
		t.Fatal(err)
	}
	if q.spill == nil {
		t.Fatal("output was not spooled")
	}
	defer os.Remove(q.spill.Name())
	defer q.spill.Close()
	info, err := q.spill.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatal("spooled public output is accessible to other users")
	}
	var direct, spooled bytes.Buffer
	if err := writeFrame(json.NewEncoder(&direct), batch); err != nil {
		t.Fatal(err)
	}
	if err := replayFrames(json.NewEncoder(&spooled), q.spill); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(direct.Bytes(), spooled.Bytes()) {
		t.Fatalf("spool changed reply/reference/error facts:\n%s\n%s", direct.String(), spooled.String())
	}
}
