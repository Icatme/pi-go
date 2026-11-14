package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	pigo "github.com/Icatme/pi-go/pkg/pigo"
)

func TestPigoSignedPartsSurviveAgentPersistence(t *testing.T) {
	want := []pigo.ContentBlock{
		pigo.TextContent{Text: "answer", TextSignature: "text-sig"},
		pigo.TextContent{Text: "", TextSignature: "empty-sig"},
		pigo.TextContent{Text: " \n", TextSignature: "space-sig"},
		pigo.TextContent{TextSignature: "absent-sig", SignatureOnly: true},
		pigo.ThinkingContent{ThinkingSignature: "empty-thought-sig"},
		pigo.ThinkingContent{ThinkingSignature: "absent-thought-sig", SignatureOnly: true},
	}
	message := convertPigoAssistantSnapshot(pigo.AssistantMessage{
		API: "google-generative-ai", Provider: "google", Model: "fixture",
		Content: want, StopReason: pigo.StopReasonStop,
	})
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var restored Message
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	got := convertAssistantMessageToPigo(restored).(pigo.AssistantMessage)
	if !reflect.DeepEqual(got.Content, want) {
		t.Fatalf("signed content changed through agent persistence: got %#v, want %#v", got.Content, want)
	}
	if blocks := convertPartsToPigoBlocks([]Part{{Type: PartTypeText, Text: " \n"}}); len(blocks) != 0 {
		t.Fatal("unsigned whitespace should retain its existing filtering")
	}
}
