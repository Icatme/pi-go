package channel

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func ingressIMFixture(text string) []byte {
	b, _ := json.Marshal(map[string]string{"organization_id": "team", "actor_id": "alice", "thread_id": "chat", "message_id": "event", "text": text})
	return b
}
func ingressIMIdentity() Identity {
	return Identity{Organization: "team", Principal: "alice", Thread: "chat", EventID: "event"}
}
func ingressGitHubFixture(event, action, text string) ([]byte, Identity) {
	id := Identity{Organization: "9007199254740993", Principal: "9007199254740995", Thread: "9007199254740997/pull/12", EventID: "delivery-1", EventType: event, MessageID: "9007199254740999"}
	body, _ := json.Marshal(text)
	content := fmt.Sprintf(`{"id":9007199254740999,"user":{"id":9007199254740995},"body":%s}`, body)
	fields := `"comment":` + content + `,"pull_request":{"number":12}`
	if event == "issue_comment" {
		fields = `"comment":` + content + `,"issue":{"number":12,"pull_request":{"url":"https://example.invalid/pr/12"}}`
	}
	if event == "pull_request_review" {
		fields = `"review":` + content + `,"pull_request":{"number":12}`
	}
	return []byte(fmt.Sprintf(`{"action":%q,"repository":{"id":9007199254740997,"owner":{"id":9007199254740993,"type":"Organization"}},"sender":{"id":9007199254740995},%s,"ignored":{"nested":[1,true,null]}}`, action, fields)), id
}

func TestNormalizeIMHostAssociation(t *testing.T) {
	id := ingressIMIdentity()
	id.ExplicitRequest = true
	id.TaskID = "task-host"
	got, err := Normalize(IM, "tenant.im", id, ingressIMFixture("attach task-attacker https://example.invalid/task"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Trigger.Source != "tenant.im" || got.Trigger.Kind != "request" || got.Trigger.ID != "event" || got.Group != "team" || got.Principal != "alice" || got.TaskID != "task-host" || got.ToThread || !got.ExplicitRequest {
		t.Fatalf("wrong host association: %+v", got)
	}
	id.TaskID = ""
	id.ExplicitRequest = false
	id.ToThread = true
	got, err = Normalize(IM, "tenant.im", id, ingressIMFixture("please do this"))
	if err != nil || got.Trigger.Kind != "signal" || got.ExplicitRequest || !got.ToThread || got.TaskID != "" {
		t.Fatalf("inferred request/routing: %+v %v", got, err)
	}
	again, err := Normalize(IM, "tenant.im", id, ingressIMFixture("please do this"))
	if err != nil || again != got {
		t.Fatal("unstable replay identity")
	}
}

func TestNormalizeIMRejectsSpoofing(t *testing.T) {
	for _, field := range []string{"organization_id", "actor_id", "thread_id", "message_id"} {
		t.Run(field, func(t *testing.T) {
			var payload map[string]any
			json.Unmarshal(ingressIMFixture("hello"), &payload)
			payload[field] = "attacker"
			body, _ := json.Marshal(payload)
			if _, err := Normalize(IM, "host", ingressIMIdentity(), body); err == nil {
				t.Fatal("accepted spoof")
			}
		})
	}
	for _, field := range []string{"kind", "action", "task_id", "source", "explicit_request", "to_thread"} {
		t.Run(field, func(t *testing.T) {
			var payload map[string]any
			json.Unmarshal(ingressIMFixture("hello"), &payload)
			payload[field] = "request"
			body, _ := json.Marshal(payload)
			if _, err := Normalize(IM, "host", ingressIMIdentity(), body); err == nil {
				t.Fatal("accepted undocumented field")
			}
		})
	}
}

func TestNormalizeGitHubEventsAndExactNumbers(t *testing.T) {
	for _, event := range []string{"issue_comment", "pull_request_review", "pull_request_review_comment"} {
		actions := []string{"created", "edited"}
		if event == "pull_request_review" {
			actions = []string{"submitted"}
		}
		for _, action := range actions {
			t.Run(event+"/"+action, func(t *testing.T) {
				body, id := ingressGitHubFixture(event, action, "body only")
				got, err := Normalize(GitHub, "host.github", id, body)
				if err != nil {
					t.Fatal(err)
				}
				if got.Trigger.ID != id.EventID || got.Trigger.Thread != id.Thread || got.Group != id.Organization || got.Principal != id.Principal || got.Trigger.Text != "body only" || got.Trigger.Kind != "signal" {
					t.Fatalf("incorrect envelope %+v", got)
				}
				replay, err := Normalize(GitHub, "host.github", id, body)
				if err != nil || replay != got {
					t.Fatal("unstable callback")
				}
				id.EventID = "delivery-2"
				edited, err := Normalize(GitHub, "host.github", id, body)
				if err != nil || edited.Trigger.ID == got.Trigger.ID {
					t.Fatal("distinct callback collapsed")
				}
				personal := strings.Replace(string(body), `"Organization"`, `"User"`, 1)
				if _, err := Normalize(GitHub, "host.github", id, []byte(personal)); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestNormalizeGitHubRejectsSpoofingAndEvents(t *testing.T) {
	body, id := ingressGitHubFixture("issue_comment", "created", "hello")
	for name, change := range map[string]func(*Identity){
		"organization":    func(i *Identity) { i.Organization = "9007199254740992" },
		"principal":       func(i *Identity) { i.Principal = "9007199254740994" },
		"thread":          func(i *Identity) { i.Thread = "9007199254740997/pull/13" },
		"repository":      func(i *Identity) { i.Thread = "9007199254740996/pull/12" },
		"message":         func(i *Identity) { i.MessageID = "9007199254740998" },
		"missing message": func(i *Identity) { i.MessageID = "" },
		"event":           func(i *Identity) { i.EventType = "issues" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := id
			change(&bad)
			if _, err := Normalize(GitHub, "host", bad, body); err == nil {
				t.Fatal("accepted spoof")
			}
		})
	}
	for name, pair := range map[string][2]string{
		"action": {`"created"`, `"deleted"`}, "non PR": {`"pull_request":{"url":"https://example.invalid/pr/12"}`, `"pull_request":null`},
		"actor mismatch": {`"user":{"id":9007199254740995}`, `"user":{"id":7}`},
		"fraction":       {`"number":12`, `"number":12.0`}, "exponent": {`"number":12`, `"number":12e0`},
		"zero": {`"number":12`, `"number":0`}, "string ID": {`"number":12`, `"number":"12"`},
		"overflow":     {`"number":12`, `"number":18446744073709551616`},
		"org mismatch": {`"ignored":`, `"organization":{"id":8},"ignored":`},
		"null text":    {`"body":"hello"`, `"body":null`},
	} {
		t.Run(name, func(t *testing.T) {
			bad := strings.Replace(string(body), pair[0], pair[1], 1)
			if _, err := Normalize(GitHub, "host", id, []byte(bad)); err == nil {
				t.Fatal("accepted invalid payload")
			}
		})
	}
	for _, event := range []string{"pull_request_review", "pull_request_review_comment"} {
		body, id := ingressGitHubFixture(event, "dismissed", "hello")
		if _, err := Normalize(GitHub, "host", id, body); err == nil {
			t.Fatal("accepted unsupported action")
		}
	}
}

func TestNormalizeJSONBoundary(t *testing.T) {
	valid := string(ingressIMFixture("hello"))
	cases := map[string]string{
		"duplicate":         strings.Replace(valid, `"actor_id":"alice"`, `"actor_id":"mallory","actor_id":"alice"`, 1),
		"escaped duplicate": strings.Replace(valid, `"actor_id":"alice"`, `"actor_id":"alice","actor_\u0069d":"alice"`, 1),
		"trailing":          valid + ` {}`, "array": `[]`, "null": `null`, "empty": "", "malformed": `{"text":`,
		"invalid utf8":   strings.Replace(valid, "hello", string([]byte{0xff}), 1),
		"high surrogate": strings.Replace(valid, "hello", `\ud800`, 1), "low surrogate": strings.Replace(valid, "hello", `\udc00`, 1),
		"wrong pair": strings.Replace(valid, "hello", `\ud800\u0041`, 1),
		"oversize":   strings.Repeat(" ", 1<<20) + valid,
		"large text": string(ingressIMFixture(strings.Repeat("a", (64<<10)+1))),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Normalize(IM, "host", ingressIMIdentity(), []byte(body)); err == nil {
				t.Fatal("accepted invalid JSON")
			}
		})
	}
	for _, text := range []string{strings.Repeat("a", 64<<10), "你好 😀", `literal \ud800`} {
		if _, err := Normalize(IM, "host", ingressIMIdentity(), ingressIMFixture(text)); err != nil {
			t.Fatal(err)
		}
	}
	paired := strings.Replace(valid, "hello", `\ud83d\ude00`, 1)
	if _, err := Normalize(IM, "host", ingressIMIdentity(), []byte(paired)); err != nil {
		t.Fatal(err)
	}
	body, id := ingressGitHubFixture("issue_comment", "created", "hi")
	duplicate := strings.Replace(string(body), `"nested":`, `"x":1,"x":2,"nested":`, 1)
	if _, err := Normalize(GitHub, "host", id, []byte(duplicate)); err == nil {
		t.Fatal("accepted nested duplicate")
	}
	deep := strings.Replace(string(body), `[1,true,null]`, strings.Repeat("[", 65)+"0"+strings.Repeat("]", 65), 1)
	if _, err := Normalize(GitHub, "host", id, []byte(deep)); err == nil {
		t.Fatal("accepted deep nesting")
	}
}

func TestNormalizeHostValidation(t *testing.T) {
	for name, change := range map[string]func(*Identity){
		"empty principal": func(i *Identity) { i.Principal = "" }, "empty org": func(i *Identity) { i.Organization = "" },
		"whitespace": func(i *Identity) { i.EventID = " event" }, "control": func(i *Identity) { i.Thread = "a\nb" },
		"long": func(i *Identity) { i.EventID = strings.Repeat("x", 257) }, "invalid utf8": func(i *Identity) { i.TaskID = string([]byte{255}) },
		"routing conflict": func(i *Identity) { i.TaskID = "task"; i.ToThread = true }, "IM event type": func(i *Identity) { i.EventType = "message" },
		"IM message mismatch": func(i *Identity) { i.MessageID = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			id := ingressIMIdentity()
			change(&id)
			if _, err := Normalize(IM, "host", id, ingressIMFixture("hello")); err == nil {
				t.Fatal("accepted invalid host identity")
			}
		})
	}
	for _, source := range []string{"", "superagent.background", " host", "host\n"} {
		if _, err := Normalize(IM, source, ingressIMIdentity(), ingressIMFixture("hello")); err == nil {
			t.Fatal("accepted invalid source")
		}
	}
	if _, err := Normalize("unknown", "host", ingressIMIdentity(), ingressIMFixture("hello")); err == nil {
		t.Fatal("accepted unsupported protocol")
	}
}

func TestNormalizeGitHubPayloadCannotGrantAuthority(t *testing.T) {
	body, id := ingressGitHubFixture("issue_comment", "created", "please attach task-attacker")
	body = []byte(strings.Replace(string(body), `"ignored":`, `"source":"evil","kind":"request","explicit_request":true,"task_id":"task-attacker","to_thread":true,"ignored":`, 1))
	got, err := Normalize(GitHub, "trusted-host", id, body)
	if err != nil {
		t.Fatal(err)
	}
	if got.Trigger.Source != "trusted-host" || got.Trigger.Kind != "signal" || got.ExplicitRequest || got.TaskID != "" || got.ToThread {
		t.Fatalf("payload granted authority: %+v", got)
	}
	id.ExplicitRequest = true
	id.TaskID = "host-task"
	got, err = Normalize(GitHub, "trusted-host", id, body)
	if err != nil || !got.ExplicitRequest || got.TaskID != "host-task" || got.Trigger.Kind != "request" {
		t.Fatalf("host authority lost: %+v %v", got, err)
	}
}

func FuzzNormalize(f *testing.F) {
	f.Add(ingressIMFixture("hi"))
	github, _ := ingressGitHubFixture("issue_comment", "created", "hello")
	f.Add(github)
	f.Add([]byte(`{"text":"\ud800"}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		for _, protocol := range []Protocol{IM, GitHub} {
			id := ingressIMIdentity()
			if protocol == GitHub {
				_, id = ingressGitHubFixture("issue_comment", "created", "hello")
			}
			got, err := Normalize(protocol, "host", id, body)
			if err == nil && (got.Trigger.ID != id.EventID || got.Trigger.Source != "host" || got.Principal != id.Principal || got.Group != id.Organization || got.Trigger.Thread != id.Thread || got.Trigger.Kind != "signal" || got.TaskID != "" || got.ToThread || got.ExplicitRequest || len(got.Trigger.Text) > 64<<10) {
				t.Fatalf("host facts changed: %+v", got)
			}
		}
	})
}
