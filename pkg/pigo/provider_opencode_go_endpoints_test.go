package pigo

import "testing"

func TestOpenCodeGoDocumentedProtocolEndpoints(t *testing.T) {
	for _, tc := range []struct{ id, want string }{
		{"kimi-k3", "https://opencode.ai/inference/go/openai/v1"},
		{"gpt-5.6-luna", "https://opencode.ai/inference/go/openai/v1"},
		{"minimax-m3", "https://opencode.ai/inference/go/anthropic"},
	} {
		model := GetModel("opencode-go", tc.id)
		if model == nil || model.BaseURL != tc.want {
			t.Errorf("%s: got %+v; want base %s", tc.id, model, tc.want)
		}
	}
}
