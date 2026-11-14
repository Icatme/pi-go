package pigo

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestAnthropicLongCacheExplicitCompat(t *testing.T) {
	yes, no := true, false
	for _, provider := range []Provider{"anthropic", "kimi-coding", "custom"} {
		for _, url := range []string{"https://api.anthropic.com", "https://proxy.example"} {
			for _, retention := range []CacheRetention{CacheRetentionLong, CacheRetentionShort, CacheRetentionNone} {
				for _, tc := range []struct {
					name   string
					compat ProviderCompat
					flag   *bool
				}{{"none", nil, nil}, {"nil", &AnthropicMessagesCompat{}, nil}, {"true", &AnthropicMessagesCompat{SupportsLongCacheRetention: &yes}, &yes}, {"false", &AnthropicMessagesCompat{SupportsLongCacheRetention: &no}, &no}, {"wrongtype", &OpenAIResponsesCompat{SupportsLongCacheRetention: &yes}, nil}} {
					t.Run(fmt.Sprintf("%s/%s/%s/%s", provider, url, retention, tc.name), func(t *testing.T) {
						req := buildAnthropicRequest(Model{ID: "fixture", Provider: provider, API: "anthropic-messages", BaseURL: url, Compat: tc.compat}, Context{SystemPrompt: "cache me"}, AnthropicMessagesProviderOptions{StreamOptions: StreamOptions{CacheRetention: retention}}, false, true)
						raw, err := json.Marshal(req)
						if err != nil {
							t.Fatal(err)
						}
						want := retention == CacheRetentionLong && strings.Contains(url, "api.anthropic.com")
						if retention == CacheRetentionLong && tc.flag != nil {
							want = *tc.flag
						}
						if got := strings.Contains(string(raw), `"ttl":"1h"`); got != want {
							t.Fatalf("got%v want%v %s", got, want, raw)
						}
						hasCache := strings.Contains(string(raw), `"cache_control"`)
						if hasCache != (retention != CacheRetentionNone) {
							t.Fatalf("cache mode changed %s", raw)
						}
						if req.Model != "fixture" {
							t.Fatal("identity changed")
						}
					})
				}
			}
		}
	}
}

func TestAnthropicLongCacheCompatClone(t *testing.T) {
	yes := true
	original := &AnthropicMessagesCompat{SupportsLongCacheRetention: &yes}
	copied := cloneCompat(original).(*AnthropicMessagesCompat)
	if copied.SupportsLongCacheRetention == original.SupportsLongCacheRetention || !*copied.SupportsLongCacheRetention {
		t.Fatal("clone aliased")
	}
	*copied.SupportsLongCacheRetention = false
	if !yes {
		t.Fatal("original changed")
	}
}
