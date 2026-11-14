package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestGoogleSignedPartsStreamReplay(t *testing.T) {
	cases := []struct {
		name  string
		parts []string
		want  string
	}{
		{"trailing empty", []string{`{"text":"Hello "}`, `{"text":"world"}`, `{"text":"","thoughtSignature":"QUFBQQ=="}`}, `[{"text":"Hello world"},{"text":"","thoughtSignature":"QUFBQQ=="}]`},
		{"two signed", []string{`{"text":"First.","thoughtSignature":"QUFBQQ=="}`, `{"text":" Second.","thoughtSignature":"QkJCQg=="}`}, `[{"text":"First.","thoughtSignature":"QUFBQQ=="},{"text":" Second.","thoughtSignature":"QkJCQg=="}]`},
		{"signed unsigned signed", []string{`{"text":"a","thoughtSignature":"QUFBQQ=="}`, `{"text":"b"}`, `{"text":"c","thoughtSignature":"QkJCQg=="}`}, `[{"text":"a","thoughtSignature":"QUFBQQ=="},{"text":"b"},{"text":"c","thoughtSignature":"QkJCQg=="}]`},
		{"signature only", []string{`{"text":"Answer"}`, `{"thoughtSignature":"Q0NDQw=="}`, `{"text":"after"}`}, `[{"text":"Answer"},{"thoughtSignature":"Q0NDQw=="},{"text":"after"}]`},
		{"thinking boundaries", []string{`{"thought":true,"text":"plan"}`, `{"thought":true,"text":"","thoughtSignature":"QUFBQQ=="}`, `{"thought":true,"thoughtSignature":"QkJCQg=="}`, `{"text":"answer"}`}, `[{"thought":true,"text":"plan"},{"thought":true,"text":"","thoughtSignature":"QUFBQQ=="},{"thought":true,"thoughtSignature":"QkJCQg=="},{"text":"answer"}]`},
		{"tool signatures", []string{`{"functionCall":{"id":"call1","name":"lookup","args":{"x":1}},"thoughtSignature":"QUFBQQ=="}`, `{"functionCall":{"id":"call2","name":"lookup","args":{"x":2}},"thoughtSignature":"QkJCQg=="}`}, `[{"functionCall":{"id":"call1","name":"lookup","args":{"x":1}},"thoughtSignature":"QUFBQQ=="},{"functionCall":{"id":"call2","name":"lookup","args":{"x":2}},"thoughtSignature":"QkJCQg=="}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, p := range tc.parts {
					fmt.Fprintf(w, "data: {\"candidates\":[{\"content\":{\"parts\":[%s]}}]}\n\n", p)
				}
				fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[]},\"finishReason\":\"STOP\"}]}\n\n")
			}))
			defer server.Close()
			model := Model{ID: "gemini-test", Provider: "google", API: "google-generative-ai", BaseURL: server.URL}
			stream := streamGoogle(model, Context{Messages: []Message{UserMessage{Content: "hi"}}}, ProviderStreamOptions{APIKey: "test"})
			events := collectAssistantMessageEvents(stream.Events())
			result := stream.Result()
			if result.StopReason == StopReasonError {
				t.Fatal(result.ErrorMessage)
			}
			if !hasEventType(events, AssistantMessageEventDone) {
				t.Fatal("missing done")
			}
			// Exercise durable Context history and the full transformation pipeline,
			// not just direct conversion of the parser's in-memory result.
			data, err := SerializeContext(Context{Messages: []Message{result}})
			if err != nil {
				t.Fatal(err)
			}
			restored, err := DeserializeContext(data)
			if err != nil {
				t.Fatal(err)
			}
			contents := convertGoogleMessages(model, restored)
			got, err := json.Marshal(contents[0].Parts)
			if err != nil {
				t.Fatal(err)
			}
			var gv, wv any
			if err = json.Unmarshal(got, &gv); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal([]byte(tc.want), &wv); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gv, wv) {
				t.Fatalf("got %s; want %s", got, tc.want)
			}
			for _, target := range []Model{{ID: "other", Provider: model.Provider, API: model.API}, {ID: model.ID, Provider: "other", API: model.API}, {ID: model.ID, Provider: model.Provider, API: "other"}} {
				parts := convertGoogleMessages(target, restored)
				wire, _ := json.Marshal(parts)
				var check []map[string]any
				_ = json.Unmarshal(wire, &check)
				for _, c := range check {
					for _, p := range c["parts"].([]any) {
						if _, ok := p.(map[string]any)["thoughtSignature"]; ok {
							t.Fatalf("signature leaked across model/provider/API: %s", wire)
						}
					}
				}
			}
		})
	}
}
