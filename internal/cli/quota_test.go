package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icatme/pi-go/pkg/pigo"
)

func TestRunQuotaPassesRuntimeAuthAndContext(t *testing.T) {
	t.Chdir(t.TempDir())
	provider := pigo.Provider("quota-provider")
	if err := saveAuth(filepath.Join(".pigo", "auth.json"), map[string]storedOAuthCredentials{
		string(provider): {Type: "oauth", Access: "quota-test-token", Expires: 1710000000123},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	stubQuotaQuery(t, func(gotCtx context.Context, gotProvider pigo.Provider, options pigo.QuotaQueryOptions) (pigo.QuotaResult, error) {
		calls++
		if gotCtx != ctx || gotProvider != provider {
			t.Fatalf("query did not preserve context/provider")
		}
		auth := options.Auth[provider]
		if auth.OAuth == nil || auth.OAuth.AccessToken != "quota-test-token" {
			t.Fatal("query did not receive stored runtime auth")
		}
		if options.APIKey != "" || options.BaseURL != "" || options.HTTPClient != nil {
			t.Fatal("CLI unexpectedly overrode provider query settings")
		}
		return pigo.QuotaResult{Provider: provider}, nil
	})
	previousRefresh := refreshCommandCodeModelsFn
	previousComplete := completeSimpleFn
	t.Cleanup(func() {
		refreshCommandCodeModelsFn = previousRefresh
		completeSimpleFn = previousComplete
	})
	refreshCommandCodeModelsFn = nil
	completeSimpleFn = nil

	var stdout, stderr bytes.Buffer
	if code := Run(ctx, []string{"quota", string(provider)}, nil, &stdout, &stderr); code != 0 || calls != 1 {
		t.Fatalf("code=%d calls=%d stderr=%q", code, calls, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Provider: quota-provider") || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunQuotaValidatesArgumentsBeforeQuery(t *testing.T) {
	stubQuotaQuery(t, func(context.Context, pigo.Provider, pigo.QuotaQueryOptions) (pigo.QuotaResult, error) {
		t.Fatal("invalid arguments must not query a provider")
		return pigo.QuotaResult{}, nil
	})
	for _, args := range [][]string{
		{"quota"},
		{"quota", " "},
		{"quota", "--json"},
		{"quota", "--unknown", "commandcode"},
		{"quota", "--json=invalid", "commandcode"},
		{"quota", "commandcode", "extra"},
		{"quota", "commandcode", "--json"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(context.Background(), args, nil, &stdout, &stderr); code != 1 {
				t.Fatalf("expected argument error, got %d", code)
			}
			if stdout.Len() != 0 || stderr.String() != quotaUsage {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunQuotaHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"quota", "--help"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if stdout.String() != quotaUsage || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunQuotaUnknownProvider(t *testing.T) {
	t.Chdir(t.TempDir())
	stubQuotaQuery(t, pigo.QueryQuota)
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"quota", "unknown-quota-provider"}, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("expected unsupported provider failure, got %d", code)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), pigo.ErrQuotaUnsupported.Error()) {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunQuotaPreservesZeroAndUnknownAmounts(t *testing.T) {
	t.Chdir(t.TempDir())
	zero := 0.0
	total := 3.0
	queriedAt := time.Date(2026, 9, 7, 6, 41, 0, 0, time.UTC)
	stubQuotaQuery(t, func(context.Context, pigo.Provider, pigo.QuotaQueryOptions) (pigo.QuotaResult, error) {
		return pigo.QuotaResult{
			Provider: "commandcode", QueriedAt: queriedAt,
			Subscription: &pigo.QuotaSubscription{Plan: "individual-go", Status: "active", PeriodStart: &queriedAt},
			Balances: []pigo.QuotaBalance{
				{ID: "monthly", Unit: "credits", Remaining: &zero},
				{ID: "free", Unit: "credits"},
			},
			Windows: []pigo.QuotaWindow{
				{ID: "fiveHour", Unit: "credits", Used: &zero, Limit: &total, Remaining: &total, BalanceIDs: []string{"monthly"}},
				{ID: "requests", Unit: "requests"},
			},
		}, nil
	})
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"quota", "commandcode"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{
		"Queried at: 2026-09-07T06:41:00Z",
		"Subscription: individual-go (active)",
		"Period: 2026-09-07T06:41:00Z - unknown",
		"Balances:\n",
		"monthly [credits]: remaining=0, used=unknown, total=unknown",
		"free [credits]: remaining=unknown, used=unknown, total=unknown",
		"Windows (independent limits):\n",
		"fiveHour [credits]: remaining=3, used=0, limit=3, resets=unknown, applies-to=monthly\n",
		"requests [requests]: remaining=unknown, used=unknown, limit=unknown, resets=unknown\n",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("missing %q in %q", want, stdout.String())
		}
	}
}

func TestRunQuotaJSONPreservesPartialSnapshot(t *testing.T) {
	t.Chdir(t.TempDir())
	zero := 0.0
	stubQuotaQuery(t, func(context.Context, pigo.Provider, pigo.QuotaQueryOptions) (pigo.QuotaResult, error) {
		return pigo.QuotaResult{
			Provider:    "commandcode",
			Balances:    []pigo.QuotaBalance{{ID: "monthly", Unit: "credits", Remaining: &zero}, {ID: "free", Unit: "credits"}},
			Windows:     []pigo.QuotaWindow{{ID: "fiveHour", Unit: "credits", BalanceIDs: []string{"monthly"}}, {ID: "requests", Unit: "requests"}},
			Unavailable: []pigo.QuotaIssue{{Section: "subscription", Message: "temporarily unavailable", HTTPStatus: 503}},
		}, nil
	})
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"quota", "--json", "commandcode"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var result pigo.QuotaResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not a JSON snapshot: %v", err)
	}
	if len(result.Balances) != 2 || result.Balances[0].Remaining == nil || *result.Balances[0].Remaining != 0 || result.Balances[1].Remaining != nil {
		t.Fatal("JSON did not preserve zero versus unknown")
	}
	if len(result.Windows) != 2 || len(result.Windows[0].BalanceIDs) != 1 || result.Windows[0].BalanceIDs[0] != "monthly" || result.Windows[1].BalanceIDs != nil {
		t.Fatal("JSON did not preserve known and unreported window scope")
	}
	if strings.Count(stdout.String(), "\"balanceIds\"") != 1 {
		t.Fatal("JSON must only include reported window scope")
	}
	if len(result.Unavailable) != 1 || result.Unavailable[0].HTTPStatus != 503 {
		t.Fatal("JSON did not preserve unavailable sections")
	}
	if stderr.String() != "Warning: subscription: temporarily unavailable (HTTP 503)\n" {
		t.Fatalf("expected partial-result warning, got %q", stderr.String())
	}
}

func TestRunQuotaFatalQueryError(t *testing.T) {
	t.Chdir(t.TempDir())
	stubQuotaQuery(t, func(context.Context, pigo.Provider, pigo.QuotaQueryOptions) (pigo.QuotaResult, error) {
		return pigo.QuotaResult{}, pigo.ErrQuotaCredentials
	})
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"quota", "--json", "commandcode"}, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("expected fatal error, got %d", code)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), pigo.ErrQuotaCredentials.Error()) {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunQuotaOutputErrors(t *testing.T) {
	t.Chdir(t.TempDir())
	stubQuotaQuery(t, func(context.Context, pigo.Provider, pigo.QuotaQueryOptions) (pigo.QuotaResult, error) {
		return pigo.QuotaResult{Provider: "commandcode"}, nil
	})
	for _, jsonOutput := range []bool{false, true} {
		for _, shortWrite := range []bool{false, true} {
			args := []string{"quota", "commandcode"}
			if jsonOutput {
				args = []string{"quota", "--json", "commandcode"}
			}
			var stderr bytes.Buffer
			if code := Run(context.Background(), args, nil, quotaFailWriter{shortWrite: shortWrite}, &stderr); code != 1 {
				t.Fatalf("expected output error, got %d", code)
			}
			if !strings.Contains(stderr.String(), "Failed to write quota output:") {
				t.Fatalf("missing output error: %q", stderr.String())
			}
		}
	}
}

func TestRunQuotaWarningOutputError(t *testing.T) {
	t.Chdir(t.TempDir())
	stubQuotaQuery(t, func(context.Context, pigo.Provider, pigo.QuotaQueryOptions) (pigo.QuotaResult, error) {
		return pigo.QuotaResult{Provider: "commandcode", Unavailable: []pigo.QuotaIssue{{Section: "subscription", Message: "unavailable"}}}, nil
	})
	if code := Run(context.Background(), []string{"quota", "commandcode"}, nil, io.Discard, quotaFailWriter{}); code != 1 {
		t.Fatalf("expected warning output failure, got %d", code)
	}
}

func stubQuotaQuery(t *testing.T, query func(context.Context, pigo.Provider, pigo.QuotaQueryOptions) (pigo.QuotaResult, error)) {
	t.Helper()
	previous := queryQuotaFn
	queryQuotaFn = query
	t.Cleanup(func() { queryQuotaFn = previous })
}

type quotaFailWriter struct{ shortWrite bool }

func (w quotaFailWriter) Write(value []byte) (int, error) {
	if w.shortWrite {
		return len(value) - 1, nil
	}
	return 0, errors.New("output unavailable")
}
