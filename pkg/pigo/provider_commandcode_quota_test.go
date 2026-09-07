package pigo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const quotaTestCredits = `{"credits":{"monthlyCredits":0.5,"purchasedCredits":1.25,"freeCredits":0.25},"windowLimits":{"limited":true,"fiveHour":{"used":0,"cap":3,"resetAt":0},"weekly":{"used":3.40667886,"cap":6,"resetAt":1788973970245}}}`
const quotaTestSubscription = `{"data":{"planId":"individual-go","status":"active","currentPeriodStart":"2026-08-26T00:56:43Z","currentPeriodEnd":"2026-09-26T00:56:43Z"}}`

type quotaTestResponse struct {
	status int
	body   string
}

func quotaTestServer(t *testing.T, overrides map[string]quotaTestResponse, check func(*http.Request)) *httptest.Server {
	t.Helper()
	defaults := map[string]quotaTestResponse{
		"/alpha/whoami":                {http.StatusOK, `{"user":{"userName":"test"}}`},
		"/alpha/billing/credits":       {http.StatusOK, quotaTestCredits},
		"/alpha/billing/subscriptions": {http.StatusOK, quotaTestSubscription},
	}
	for path, response := range overrides {
		defaults[path] = response
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("quota must be read-only, got method %s", r.Method)
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Error("expected JSON accept header")
		}
		if check != nil {
			check(r)
		}
		response, ok := defaults[r.URL.Path]
		if !ok {
			t.Errorf("unexpected account request path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.status)
		_, _ = io.WriteString(w, response.body)
	}))
	t.Cleanup(server.Close)
	return server
}

func queryQuotaTestServer(ctx context.Context, server *httptest.Server) (QuotaResult, error) {
	return QueryQuota(ctx, "commandcode", QuotaQueryOptions{APIKey: "quota-test-secret", BaseURL: server.URL, HTTPClient: server.Client()})
}

func requireQuotaAmount(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil || math.Abs(*got-want) > 1e-8 {
		t.Fatalf("%s: got %v, want %v", name, got, want)
	}
}

func requireQuotaBalance(t *testing.T, result QuotaResult) {
	t.Helper()
	want := map[string]float64{"monthly": 0.5, "purchased": 1.25, "free": 0.25}
	if len(result.Balances) != len(want) {
		t.Fatalf("expected three independent credit pools: %+v", result.Balances)
	}
	for _, balance := range result.Balances {
		amount, ok := want[balance.ID]
		if !ok || balance.Unit != "credits" {
			t.Fatalf("unexpected or duplicate credit pool: %+v", balance)
		}
		requireQuotaAmount(t, balance.ID+" balance", balance.Remaining, amount)
		if balance.Total != nil || balance.Used != nil {
			t.Fatalf("balance endpoint does not report pool total or amount used: %+v", balance)
		}
		delete(want, balance.ID)
	}
}

func requireSafeQuotaError(t *testing.T, err error, status int, forbidden ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected quota query error")
	}
	var queryErr *QuotaQueryError
	if !errors.As(err, &queryErr) || queryErr.Provider != "commandcode" || queryErr.HTTPStatus != status {
		t.Fatalf("expected safe typed CommandCode error with status %d, got %v", status, err)
	}
	for _, value := range forbidden {
		if strings.Contains(err.Error(), value) {
			t.Fatalf("quota error exposed private request/response data: %q", value)
		}
	}
}

func TestCommandCodeQuotaBalancesAndWindows(t *testing.T) {
	for _, orgID := range []string{"", "org/+?"} {
		t.Run(fmt.Sprintf("org=%s", orgID), func(t *testing.T) {
			overrides := map[string]quotaTestResponse{}
			if orgID != "" {
				overrides["/alpha/whoami"] = quotaTestResponse{http.StatusOK, `{"org":{"id":"org/+?"}}`}
			}
			var requests atomic.Int32
			server := quotaTestServer(t, overrides, func(r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Authorization") != "Bearer quota-test-secret" {
					t.Error("API key was not forwarded as bearer authentication")
				}
				if r.URL.Path == "/alpha/whoami" {
					if r.URL.RawQuery != "" {
						t.Error("whoami must run before resolving organization scope")
					}
				} else if r.URL.Query().Get("orgId") != orgID || (orgID == "" && r.URL.Query().Has("orgId")) {
					t.Error("quota query used the wrong account scope")
				}
			})
			if !SupportsQuotaQuery("commandcode") {
				t.Fatal("CommandCode quota capability is not registered")
			}
			result, err := queryQuotaTestServer(context.Background(), server)
			if err != nil {
				t.Fatal(err)
			}
			requireQuotaBalance(t, result)
			if result.Provider != "commandcode" || result.QueriedAt.IsZero() || len(result.Unavailable) != 0 || requests.Load() != 3 {
				t.Fatalf("unexpected snapshot metadata or requests: %+v, calls=%d", result, requests.Load())
			}
			windows := make(map[string]QuotaWindow)
			for _, window := range result.Windows {
				windows[window.ID] = window
			}
			if len(windows) != 2 {
				t.Fatalf("expected two independent window limits: %+v", result.Windows)
			}
			fiveHour, weekly := windows["fiveHour"], windows["weekly"]
			if fiveHour.Unit != "credits" || weekly.Unit != "credits" || fiveHour.ResetsAt != nil {
				t.Fatalf("unexpected units or inactive reset time: %+v", result.Windows)
			}
			requireQuotaAmount(t, "five-hour used", fiveHour.Used, 0)
			requireQuotaAmount(t, "five-hour limit", fiveHour.Limit, 3)
			requireQuotaAmount(t, "five-hour remaining", fiveHour.Remaining, 3)
			requireQuotaAmount(t, "weekly used", weekly.Used, 3.40667886)
			requireQuotaAmount(t, "weekly remaining", weekly.Remaining, 2.59332114)
			if weekly.ResetsAt == nil || !weekly.ResetsAt.Equal(time.UnixMilli(1788973970245)) {
				t.Fatalf("resetAt must be interpreted as Unix milliseconds: %+v", weekly.ResetsAt)
			}
			if result.Subscription == nil || result.Subscription.Plan != "individual-go" || result.Subscription.Status != "active" {
				t.Fatalf("unexpected subscription: %+v", result.Subscription)
			}
			wantEnd, _ := time.Parse(time.RFC3339, "2026-09-26T00:56:43Z")
			if result.Subscription.PeriodStart == nil || result.Subscription.PeriodEnd == nil || !result.Subscription.PeriodEnd.Equal(wantEnd) {
				t.Fatalf("subscription period was not parsed: %+v", result.Subscription)
			}
		})
	}
}

func TestCommandCodeQuotaExceededWindowClampsHeadroom(t *testing.T) {
	body := strings.Replace(quotaTestCredits, `"used":0,"cap":3`, `"used":4,"cap":3`, 1)
	server := quotaTestServer(t, map[string]quotaTestResponse{"/alpha/billing/credits": {http.StatusOK, body}}, nil)
	result, err := queryQuotaTestServer(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	for _, window := range result.Windows {
		if window.ID == "fiveHour" {
			requireQuotaAmount(t, "over-limit remaining", window.Remaining, 0)
			return
		}
	}
	t.Fatal("missing five-hour window")
}

func TestCommandCodeQuotaOptionalSectionsDoNotInventZero(t *testing.T) {
	for name, override := range map[string]map[string]quotaTestResponse{
		"missing windows":          {"/alpha/billing/credits": {http.StatusOK, `{"credits":{"monthlyCredits":0.5,"purchasedCredits":1.25,"freeCredits":0.25}}`}},
		"malformed windows":        {"/alpha/billing/credits": {http.StatusOK, strings.Replace(quotaTestCredits, `"used":0`, `"used":"unknown"`, 1)}},
		"missing subscription":     {"/alpha/billing/subscriptions": {http.StatusOK, `{}`}},
		"malformed subscription":   {"/alpha/billing/subscriptions": {http.StatusOK, `{"data":{"planId":"individual-go","status":"active","currentPeriodEnd":"not-a-date"}}`}},
		"subscription unavailable": {"/alpha/billing/subscriptions": {http.StatusServiceUnavailable, `{"error":"private-account-identity"}`}},
	} {
		t.Run(name, func(t *testing.T) {
			server := quotaTestServer(t, override, nil)
			result, err := queryQuotaTestServer(context.Background(), server)
			if err != nil {
				t.Fatalf("optional unavailable section discarded valid credits: %v", err)
			}
			requireQuotaBalance(t, result)
			if len(result.Unavailable) == 0 {
				t.Fatalf("unknown data must be reported explicitly: %+v", result)
			}
			for _, issue := range result.Unavailable {
				if issue.Section == "" || issue.Message == "" || strings.Contains(issue.Message, "private-account-identity") {
					t.Fatalf("expected safe diagnostic: %+v", issue)
				}
			}
			if name == "missing windows" && len(result.Windows) != 0 {
				t.Fatal("missing window limits must not become zero amounts")
			}
			if name == "malformed windows" {
				for _, window := range result.Windows {
					if window.ID == "fiveHour" {
						t.Fatal("malformed five-hour usage must not become zero usage")
					}
				}
			}
			if name == "missing subscription" && result.Subscription != nil {
				t.Fatal("missing subscription must stay unavailable")
			}
		})
	}
}

func TestCommandCodeQuotaRejectsInvalidRequiredResponses(t *testing.T) {
	for name, override := range map[string]map[string]quotaTestResponse{
		"invalid identity JSON":     {"/alpha/whoami": {http.StatusOK, `private-account-identity`}},
		"unknown identity":          {"/alpha/whoami": {http.StatusOK, `{}`}},
		"organization without ID":   {"/alpha/whoami": {http.StatusOK, `{"user":{"userName":"test"},"org":{}}`}},
		"invalid credits JSON":      {"/alpha/billing/credits": {http.StatusOK, `private-account-identity`}},
		"missing credits":           {"/alpha/billing/credits": {http.StatusOK, `{}`}},
		"missing purchased credits": {"/alpha/billing/credits": {http.StatusOK, `{"credits":{"monthlyCredits":0.5,"freeCredits":0}}`}},
		"null free credits":         {"/alpha/billing/credits": {http.StatusOK, `{"credits":{"monthlyCredits":0.5,"purchasedCredits":0,"freeCredits":null}}`}},
		"nonnumeric credits":        {"/alpha/billing/credits": {http.StatusOK, `{"credits":{"monthlyCredits":"private-account-identity","purchasedCredits":0,"freeCredits":0}}`}},
	} {
		t.Run(name, func(t *testing.T) {
			server := quotaTestServer(t, override, nil)
			result, err := queryQuotaTestServer(context.Background(), server)
			requireSafeQuotaError(t, err, 0, "private-account-identity", "quota-test-secret", server.URL)
			if len(result.Balances) != 0 {
				t.Fatal("invalid required response returned a spendable balance")
			}
		})
	}
}

func TestCommandCodeQuotaAuthenticationFailuresAreFatal(t *testing.T) {
	for _, path := range []string{"/alpha/whoami", "/alpha/billing/credits", "/alpha/billing/subscriptions"} {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
			t.Run(fmt.Sprintf("%s/%d", path, status), func(t *testing.T) {
				server := quotaTestServer(t, map[string]quotaTestResponse{path: {status, `{"error":"private-account-identity quota-test-secret"}`}}, nil)
				_, err := queryQuotaTestServer(context.Background(), server)
				requireSafeQuotaError(t, err, status, "private-account-identity", "quota-test-secret", server.URL)
			})
		}
	}
}

func TestCommandCodeQuotaCredentialAndBasePrecedence(t *testing.T) {
	t.Setenv("COMMANDCODE_API_KEY", "environment-test-key")
	for _, tc := range []struct {
		name    string
		apiKey  string
		auth    map[Provider]AuthConfig
		wantKey string
	}{
		{"explicit", "explicit-test-key", map[Provider]AuthConfig{"commandcode": {Type: AuthTypeAPIKey, APIKey: "runtime-test-key"}}, "explicit-test-key"},
		{"runtime", "", map[Provider]AuthConfig{"commandcode": {Type: AuthTypeAPIKey, APIKey: "runtime-test-key"}}, "runtime-test-key"},
		{"environment", "", nil, "environment-test-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := quotaTestServer(t, nil, func(r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+tc.wantKey {
					t.Error("credential precedence was not respected")
				}
			})
			t.Setenv("COMMANDCODE_API_BASE", "http://invalid.example.invalid")
			_, err := QueryQuota(context.Background(), "commandcode", QuotaQueryOptions{APIKey: tc.apiKey, Auth: tc.auth, BaseURL: server.URL, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("environment base", func(t *testing.T) {
		server := quotaTestServer(t, nil, nil)
		t.Setenv("COMMANDCODE_API_BASE", server.URL)
		if _, err := QueryQuota(context.Background(), "commandcode", QuotaQueryOptions{APIKey: "quota-test-secret", HTTPClient: server.Client()}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCommandCodeQuotaLocalCredentialsAndMissingKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// A literal unresolved reference bypasses dotenv lookup but is not a usable key.
	t.Setenv("COMMANDCODE_API_KEY", "COMMANDCODE_API_KEY")
	server := quotaTestServer(t, nil, func(r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer local-test-key" {
			t.Error("synthetic local credential was not used")
		}
	})
	options := QuotaQueryOptions{BaseURL: server.URL, HTTPClient: server.Client()}
	if _, err := QueryQuota(context.Background(), "commandcode", options); !errors.Is(err, ErrQuotaCredentials) {
		t.Fatalf("expected missing credentials sentinel, got %v", err)
	}
	credentialDir := filepath.Join(home, ".commandcode")
	if err := os.Mkdir(credentialDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credentialDir, "auth.json"), []byte(`{"apiKey":"local-test-key"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := QueryQuota(context.Background(), "commandcode", options); err != nil {
		t.Fatal(err)
	}
}

func TestCommandCodeQuotaCancellationAndResponseLimit(t *testing.T) {
	t.Run("caller cancellation", func(t *testing.T) {
		started := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			<-r.Context().Done()
		}))
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := queryQuotaTestServer(ctx, server)
			done <- err
		}()
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("account request did not begin")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation must survive sanitized transport error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("canceled quota query did not stop promptly")
		}
	})
	t.Run("bounded body", func(t *testing.T) {
		server := quotaTestServer(t, map[string]quotaTestResponse{"/alpha/whoami": {http.StatusOK, `{"user":{"userName":"` + strings.Repeat("x", 1<<20) + `"}}`}}, nil)
		_, err := queryQuotaTestServer(context.Background(), server)
		requireSafeQuotaError(t, err, 0, "quota-test-secret", server.URL)
	})
}

func TestCommandCodeQuotaRefusesRedirectWithoutMutatingClient(t *testing.T) {
	var destinationCalls, redirectPolicyCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationCalls.Add(1)
		_, _ = io.WriteString(w, `{"user":{"userName":"test"}}`)
	}))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/private-account-identity", http.StatusFound)
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = time.Second
	policyError := errors.New("original redirect policy")
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		redirectPolicyCalls.Add(1)
		return policyError
	}
	_, err := QueryQuota(context.Background(), "commandcode", QuotaQueryOptions{APIKey: "quota-test-secret", BaseURL: server.URL, HTTPClient: client})
	requireSafeQuotaError(t, err, http.StatusFound, "quota-test-secret", server.URL, destination.URL, "private-account-identity")
	if destinationCalls.Load() != 0 || redirectPolicyCalls.Load() != 0 {
		t.Fatalf("quota redirected or consulted unsafe caller redirect policy: destination=%d policy=%d", destinationCalls.Load(), redirectPolicyCalls.Load())
	}
	if client.Timeout != time.Second || !errors.Is(client.CheckRedirect(nil, nil), policyError) {
		t.Fatal("quota mutated the caller's HTTP client")
	}
}

func TestCommandCodeQuotaConcurrentQueriesUseIndependentSnapshots(t *testing.T) {
	server := quotaTestServer(t, nil, nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := queryQuotaTestServer(context.Background(), server)
			if err != nil {
				t.Errorf("concurrent query failed: %v", err)
				return
			}
			if len(result.Balances) != 3 || result.Balances[0].ID != "monthly" || result.Balances[0].Remaining == nil || *result.Balances[0].Remaining != 0.5 {
				t.Error("concurrent query returned inconsistent balance")
				return
			}
			*result.Balances[0].Remaining = 0
		}()
	}
	wg.Wait()
}

func TestCommandCodeQuotaExplicitEmptyAuthDoesNotChangeAccounts(t *testing.T) {
	t.Setenv("COMMANDCODE_API_KEY", "unrelated-environment-account")
	var calls atomic.Int32
	server := quotaTestServer(t, nil, func(*http.Request) { calls.Add(1) })
	_, err := QueryQuota(context.Background(), "commandcode", QuotaQueryOptions{
		Auth:    map[Provider]AuthConfig{"commandcode": {}},
		BaseURL: server.URL, HTTPClient: server.Client(),
	})
	if !errors.Is(err, ErrQuotaCredentials) || calls.Load() != 0 {
		t.Fatalf("explicit empty credentials must not select another account: err=%v calls=%d", err, calls.Load())
	}
}

func TestCommandCodeQuotaSurvivesModelCatalogRefresh(t *testing.T) {
	isolateProviderRegistry(t)
	server := quotaTestServer(t, map[string]quotaTestResponse{
		"/provider/v1/models": {http.StatusOK, `{"object":"list","data":[{"id":"quota-test-model","name":"Quota Test","context_length":1000}]}`},
	}, nil)
	t.Setenv("COMMANDCODE_MODELS_URL", server.URL+"/provider/v1/models")
	t.Setenv("COMMANDCODE_MODELS_CACHE", filepath.Join(t.TempDir(), "commandcode-models.json"))
	if _, err := RefreshCommandCodeModelsWithResult(context.Background(), server.Client()); err != nil {
		t.Fatal(err)
	}
	if GetModel("commandcode", "quota-test-model") == nil || !SupportsQuotaQuery("commandcode") {
		t.Fatal("refreshing model metadata removed account query capability")
	}
	result, err := queryQuotaTestServer(context.Background(), server)
	if err != nil {
		t.Fatalf("quota dispatch failed after catalog refresh: %v", err)
	}
	requireQuotaBalance(t, result)
}

func TestCommandCodeQuotaAbsentSubscriptionAndUnlimitedWindows(t *testing.T) {
	body := `{"credits":{"monthlyCredits":0.5,"purchasedCredits":1.25,"freeCredits":0.25},"windowLimits":{"limited":false}}`
	server := quotaTestServer(t, map[string]quotaTestResponse{
		"/alpha/billing/credits":       {http.StatusOK, body},
		"/alpha/billing/subscriptions": {http.StatusOK, `{"data":null}`},
	}, nil)
	result, err := queryQuotaTestServer(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	requireQuotaBalance(t, result)
	if len(result.Windows) != 0 || len(result.Unavailable) != 0 || result.Subscription != nil {
		t.Fatalf("explicit absence/unlimited status is not a query failure: %+v", result)
	}
}

func TestCommandCodeQuotaRejectsUnsafeBaseURLWithoutLeakingIt(t *testing.T) {
	for _, base := range []string{
		"https://private-user:quota-test-secret@api.example.invalid",
		"https://api.example.invalid?token=quota-test-secret",
		"https://api.example.invalid#quota-test-secret",
		"file:///private-account-identity",
	} {
		_, err := QueryQuota(context.Background(), "commandcode", QuotaQueryOptions{APIKey: "quota-test-secret", BaseURL: base})
		requireSafeQuotaError(t, err, 0, "quota-test-secret", "private-user", "private-account-identity", base)
	}
}

func TestCommandCodeQuotaMonthlyDebtDoesNotReducePurchasedCredits(t *testing.T) {
	body := `{"credits":{"monthlyCredits":-1,"purchasedCredits":10,"freeCredits":0},"windowLimits":{"limited":true,"fiveHour":{"used":4,"cap":3,"resetAt":0},"weekly":{"used":8,"cap":6,"resetAt":0}}}`
	server := quotaTestServer(t, map[string]quotaTestResponse{
		"/alpha/billing/credits": {http.StatusOK, body},
	}, nil)
	result, err := queryQuotaTestServer(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{"monthly": 0, "purchased": 10, "free": 0}
	if len(result.Balances) != len(want) || len(result.Windows) != 2 {
		t.Fatalf("expected independent pools and both exhausted windows: %+v", result)
	}
	for _, balance := range result.Balances {
		amount, ok := want[balance.ID]
		if !ok || balance.Unit != "credits" {
			t.Fatalf("unexpected or duplicate credit pool: %+v", balance)
		}
		requireQuotaAmount(t, balance.ID+" balance", balance.Remaining, amount)
		delete(want, balance.ID)
	}
	for _, window := range result.Windows {
		if window.Unit != "credits" || len(window.BalanceIDs) != 1 || window.BalanceIDs[0] != "monthly" {
			t.Fatalf("spending windows must only restrict the monthly pool: %+v", window)
		}
		requireQuotaAmount(t, window.ID+" headroom", window.Remaining, 0)
	}
}
