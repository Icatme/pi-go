package pigo

import (
	"context"
	"net/http"
	"testing"
)

func TestKiroIncrementalRegistrationStrictDefaultsAndGuards(t *testing.T) {
	for _, mode := range []string{"auto", "session", "websocket", "api-key", "missing-schema"} {
		t.Run(mode, func(t *testing.T) {
			base, model := kiroRegistrationFixture(t)
			calls := 0
			base.Credentials = func(context.Context, KiroCredentialScope) (KiroScopedCredential, error) {
				calls++
				return KiroScopedCredential{}, nil
			}
			base.RoundTrip = func(*http.Request) (*http.Response, error) { calls++; return nil, nil }
			if err := RegisterKiroIncrementalModule(t.Name(), KiroIncrementalConfig(base), KiroIncrementalOptions{}); err != nil {
				t.Fatal(err)
			}
			defer UnregisterAPIModules(t.Name())
			if calls != 0 {
				t.Fatal("callbacks at registration")
			}
			options := ProviderStreamOptions{RequestContext: context.Background(), Transport: TransportSSE}
			conversation := Context{Messages: []Message{UserMessage{Content: "hello"}}}
			switch mode {
			case "auto":
				options.Transport = TransportAuto
			case "session":
				options.SessionID = "session"
			case "websocket":
				options.Transport = TransportWebSocket
			case "api-key":
				options.APIKey = "PRIVATE"
			case "missing-schema":
				conversation.Tools = []Tool{{Name: "lookup"}}
			}
			stream := Stream(model, conversation, options)
			defer stream.Close()
			kiroReadHostEvents(t, stream)
			result := kiroReadHostResult(t, stream)
			if result.StopReason != StopReasonError || calls != 0 {
				t.Fatal("guard reached credentials/transport")
			}
		})
	}
}

func TestKiroIncrementalRegistrationInvalidAffinity(t *testing.T) {
	base, _ := kiroRegistrationFixture(t)
	if err := RegisterKiroIncrementalModule(t.Name(), KiroIncrementalConfig(base), KiroIncrementalOptions{SessionAffinity: &KiroSessionAffinity{Tenant: "tenant", Key: []byte("short")}}); err != errKiroIncrementalRegistration {
		t.Fatal("invalid affinity registered")
	}
	if GetAPIModule(base.API) != nil {
		t.Fatal("module installed")
	}
}
