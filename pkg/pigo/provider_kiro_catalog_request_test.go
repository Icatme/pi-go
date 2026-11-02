package pigo

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestKiroCatalogRequestExplicitWireAndAuthFamilies(t *testing.T) {
	for _, test := range []struct {
		kind             kiroCredentialKind
		token, tokenType string
	}{{kiroCredentialBearer, "LOCAL_BEARER", ""}, {kiroCredentialAPIKey, "ksk_LOCAL_ONLY", "API_KEY"}, {kiroCredentialExternalIDP, "LOCAL_IDP", "EXTERNAL_IDP"}} {
		t.Run(string(test.kind), func(t *testing.T) {
			options := kiroCatalogRequestOptions{Scope: kiroCredentialScope{Region: "us-east-1", ProfileARN: "arn:example:profile/a+b?c=d&origin=not-injected"}, Origin: "LOCAL & TEST", UserAgent: "local-test/1"}
			calls := 0
			request, err := newKiroOfflineCatalogRequest(context.Background(), options, func(_ context.Context, s kiroCredentialScope) (kiroScopedCredential, error) {
				calls++
				return kiroScopedCredential{Scope: s, Kind: test.kind, Token: test.token}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || request.Method != http.MethodGet || request.URL.Scheme != "https" || request.URL.Host != "management.us-east-1.kiro.dev" || request.URL.Path != "/List-Available-Models" || request.URL.User != nil || request.URL.Fragment != "" || request.Body != nil {
				t.Fatal("management route/body")
			}
			q := request.URL.Query()
			if len(q) != 2 || len(q["origin"]) != 1 || q.Get("origin") != options.Origin || q.Get("profileArn") != options.Scope.ProfileARN {
				t.Fatal("query escaping")
			}
			if request.Header.Get("Authorization") != "Bearer "+test.token || request.Header.Get("tokentype") != test.tokenType || request.Header.Get("Accept") != "application/json" || request.Header.Get("User-Agent") != options.UserAgent {
				t.Fatal("explicit headers")
			}
			for _, name := range []string{"Content-Type", "X-Amz-Target", "amz-sdk-request", "amz-sdk-invocation-id", "x-amzn-kiro-agent-mode"} {
				if request.Header.Get(name) != "" {
					t.Fatal("runtime or POST headers leaked")
				}
			}
			if strings.Contains(request.URL.String(), test.token) {
				t.Fatal("token in URL")
			}
		})
	}
}

func TestKiroCatalogRequestRejectsBeforeSource(t *testing.T) {
	for _, mode := range []string{"nil-context", "cancelled", "nil-source", "region", "profile", "origin", "user-agent"} {
		t.Run(mode, func(t *testing.T) {
			options := kiroCatalogRequestOptions{Scope: kiroCredentialScope{Region: "us-east-1", ProfileARN: "profile"}, Origin: "LOCAL_TEST", UserAgent: "local/1"}
			var ctx context.Context = context.Background()
			calls := 0
			source := kiroCredentialSource(func(context.Context, kiroCredentialScope) (kiroScopedCredential, error) {
				calls++
				return kiroScopedCredential{}, nil
			})
			switch mode {
			case "nil-context":
				ctx = nil
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil-source":
				source = nil
			case "region":
				options.Scope.Region = "us-east-1.attacker.invalid"
			case "profile":
				options.Scope.ProfileARN = "PRIVATE\nBAD"
			case "origin":
				options.Origin = ""
			case "user-agent":
				options.UserAgent = "injected\r\nHeader: PRIVATE"
			}
			r, err := newKiroOfflineCatalogRequest(ctx, options, source)
			if r != nil || err != errKiroCatalogRequest || calls != 0 {
				t.Fatal("preflight called source")
			}
		})
	}
}

func TestKiroCatalogRequestSourceScopeCancellationAndSnapshot(t *testing.T) {
	for _, mode := range []string{"error", "profile", "region", "cancel", "token", "mutate-options"} {
		t.Run(mode, func(t *testing.T) {
			options := kiroCatalogRequestOptions{Scope: kiroCredentialScope{Region: "eu-central-1", ProfileARN: "profile"}, Origin: "LOCAL_TEST", UserAgent: "local/1"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			r, err := newKiroOfflineCatalogRequest(ctx, options, func(_ context.Context, s kiroCredentialScope) (kiroScopedCredential, error) {
				calls++
				credential := kiroScopedCredential{Scope: s, Kind: kiroCredentialBearer, Token: "LOCAL_ONLY"}
				switch mode {
				case "error":
					return credential, errors.New("PRIVATE_SOURCE")
				case "profile":
					credential.Scope.ProfileARN = "other"
				case "region":
					credential.Scope.Region = "us-east-1"
				case "cancel":
					cancel()
				case "token":
					credential.Token = "PRIVATE\nBAD"
				case "mutate-options":
					options.Scope.ProfileARN = "changed"
					options.Scope.Region = "us-east-1"
					options.Origin = "changed"
				}
				return credential, nil
			})
			if calls != 1 {
				t.Fatal("source retried")
			}
			if mode == "mutate-options" {
				if err != nil || r.URL.Host != "management.eu-central-1.kiro.dev" || r.URL.Query().Get("profileArn") != "profile" || r.URL.Query().Get("origin") != "LOCAL_TEST" {
					t.Fatal("source changed prepared route")
				}
				return
			}
			if r != nil || err != errKiroCatalogRequest || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("failed source exposed request or secret")
			}
		})
	}
}
