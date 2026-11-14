package pigo

import "testing"

func TestIsContextOverflowInputTooLongModelLimit(t *testing.T) {
	for _, text := range []string{"400: Input too long: model limit 1048576 tokens, requested 1080887 tokens", "Input too long: model limit 1,048,576 tokens, requested 1,080,887 tokens"} {
		if !IsContextOverflow(AssistantMessage{StopReason: StopReasonError, ErrorMessage: text}, 0) {
			t.Errorf("not recognized: %s", text)
		}
	}
}
func TestIsContextOverflowInputTooLongRequiresTokenLimitEvidence(t *testing.T) {
	for _, text := range []string{"Input too long: model limit ,,, tokens", "Input too long", "Input too long: model limit 10 requests", "Input too long: model limit unknown tokens", "Input too long: model limit 1024 bytes", "Rate limit: Input too long: model limit 1024 tokens"} {
		if IsContextOverflow(AssistantMessage{StopReason: StopReasonError, ErrorMessage: text}, 0) {
			t.Errorf("false positive: %s", text)
		}
	}
	if IsContextOverflow(AssistantMessage{StopReason: StopReasonStop, ErrorMessage: "Input too long: model limit 1024 tokens"}, 0) {
		t.Fatal("successful reply classified as overflow")
	}
}
