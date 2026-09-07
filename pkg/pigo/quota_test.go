package pigo

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type quotaQueryFunc func(context.Context, QuotaQueryOptions) (QuotaResult, error)

func (f quotaQueryFunc) QueryQuota(ctx context.Context, options QuotaQueryOptions) (QuotaResult, error) {
	return f(ctx, options)
}

func TestQueryQuotaUnsupported(t *testing.T) {
	isolateProviderRegistry(t)
	RegisterProviderModule(ProviderModule{Provider: "quota-not-implemented"})
	for _, provider := range []Provider{"quota-not-registered", "quota-not-implemented"} {
		if SupportsQuotaQuery(provider) {
			t.Fatalf("%s unexpectedly advertises quota queries", provider)
		}
		if _, err := QueryQuota(context.Background(), provider, QuotaQueryOptions{}); !errors.Is(err, ErrQuotaUnsupported) {
			t.Fatalf("%s: expected unsupported sentinel, got %v", provider, err)
		}
	}
}

func TestQueryQuotaAccountOnlyProviderDispatch(t *testing.T) {
	isolateProviderRegistry(t)
	provider := Provider("quota-account-only")
	client := &http.Client{}
	options := QuotaQueryOptions{
		APIKey:  "explicit-test-key",
		Auth:    map[Provider]AuthConfig{provider: {Type: AuthTypeAPIKey, APIKey: "runtime-test-key"}},
		BaseURL: "https://quota.example.invalid", HTTPClient: client,
	}
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "account-context")
	called := false
	RegisterProviderModule(ProviderModule{
		Provider: provider,
		Quota: quotaQueryFunc(func(got context.Context, gotOptions QuotaQueryOptions) (QuotaResult, error) {
			called = true
			if got.Value(contextKey{}) != "account-context" || !reflect.DeepEqual(options, gotOptions) {
				t.Errorf("dispatch did not preserve context or account options")
			}
			deadline, ok := got.Deadline()
			if !ok || time.Until(deadline) <= 13*time.Second || time.Until(deadline) > 15*time.Second {
				t.Errorf("expected bounded default deadline, got %v, present=%v", deadline, ok)
			}
			return QuotaResult{Balances: []QuotaBalance{{ID: "credits", Unit: "credits"}}}, nil
		}),
	})
	if !SupportsQuotaQuery(provider) || len(GetModels(provider)) != 0 {
		t.Fatal("account capability must be available without any models")
	}
	before := time.Now()
	result, err := QueryQuota(ctx, provider, options)
	if err != nil || !called || result.Provider != provider || len(result.Balances) != 1 || result.QueriedAt.Before(before) {
		t.Fatalf("unexpected account query result: %+v, err=%v, called=%v", result, err, called)
	}
}

func TestQueryQuotaPreservesDeadlineAndRejectsCanceledContext(t *testing.T) {
	isolateProviderRegistry(t)
	provider := Provider("quota-context")
	deadline := time.Now().Add(time.Second)
	var calls atomic.Int32
	RegisterProviderModule(ProviderModule{Provider: provider, Quota: quotaQueryFunc(func(ctx context.Context, _ QuotaQueryOptions) (QuotaResult, error) {
		calls.Add(1)
		if got, ok := ctx.Deadline(); !ok || !got.Equal(deadline) {
			t.Errorf("caller deadline changed: %v", got)
		}
		return QuotaResult{}, nil
	})})
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if _, err := QueryQuota(ctx, provider, QuotaQueryOptions{}); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := QueryQuota(ctx, provider, QuotaQueryOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("canceled query reached provider: %d calls", calls.Load())
	}
}

func TestQueryQuotaConcurrentCallers(t *testing.T) {
	isolateProviderRegistry(t)
	provider := Provider("quota-concurrent")
	var calls atomic.Int32
	RegisterProviderModule(ProviderModule{Provider: provider, Quota: quotaQueryFunc(func(context.Context, QuotaQueryOptions) (QuotaResult, error) {
		calls.Add(1)
		return QuotaResult{}, nil
	})})
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := QueryQuota(context.Background(), provider, QuotaQueryOptions{})
			if err != nil || result.Provider != provider || result.QueriedAt.IsZero() {
				t.Errorf("concurrent account query failed: %+v, %v", result, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 16 {
		t.Fatalf("expected independent queries, got %d", calls.Load())
	}
}
