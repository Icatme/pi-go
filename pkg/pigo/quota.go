package pigo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

var (
	ErrQuotaUnsupported = errors.New("provider does not support quota queries")
	ErrQuotaCredentials = errors.New("missing credentials for quota query")
)

// QuotaQuerier is an optional account capability on ProviderModule. It must be
// safe for concurrent calls and must not invoke a model or mutate account state.
// Implementations return unavailable sections explicitly, never as zero usage.
type QuotaQuerier interface {
	QueryQuota(context.Context, QuotaQueryOptions) (QuotaResult, error)
}

// QuotaQueryOptions is account-scoped, independent of model generation options.
// APIKey overrides Auth. Providers may use their normal local credential lookup
// when neither is supplied. BaseURL is an explicit, trusted account API override.
type QuotaQueryOptions struct {
	APIKey     string
	Auth       map[Provider]AuthConfig
	BaseURL    string
	HTTPClient *http.Client
}

// QuotaResult is a live snapshot. Balances and window headroom are independent
// constraints and must not be added together. A nil amount or timestamp means
// unknown; a non-nil zero amount means a known normalized zero.
type QuotaResult struct {
	Provider     Provider           `json:"provider"`
	QueriedAt    time.Time          `json:"queriedAt"`
	Balances     []QuotaBalance     `json:"balances"`
	Windows      []QuotaWindow      `json:"windows"`
	Subscription *QuotaSubscription `json:"subscription,omitempty"`
	Unavailable  []QuotaIssue       `json:"unavailable,omitempty"`
}

// QuotaBalance describes one credit pool. Unit is provider-defined, such as USD
// or tokens; amounts in different units must not be combined.
type QuotaBalance struct {
	ID        string   `json:"id"`
	Unit      string   `json:"unit"`
	Total     *float64 `json:"total,omitempty"`
	Used      *float64 `json:"used,omitempty"`
	Remaining *float64 `json:"remaining,omitempty"`
}

// QuotaWindow describes a rate or spending window, not an additional balance.
type QuotaWindow struct {
	// BalanceIDs identifies the pools affected by this window. Nil means the
	// provider did not report a pool scope; it does not mean all balances.
	BalanceIDs []string   `json:"balanceIds,omitempty"`
	ID         string     `json:"id"`
	Unit       string     `json:"unit"`
	Limit      *float64   `json:"limit,omitempty"`
	Used       *float64   `json:"used,omitempty"`
	Remaining  *float64   `json:"remaining,omitempty"`
	ResetsAt   *time.Time `json:"resetsAt,omitempty"`
}

type QuotaSubscription struct {
	Plan        string     `json:"plan"`
	Status      string     `json:"status"`
	PeriodStart *time.Time `json:"periodStart,omitempty"`
	PeriodEnd   *time.Time `json:"periodEnd,omitempty"`
}

// QuotaIssue identifies an unavailable section of an otherwise usable snapshot.
// Messages must not contain credentials, response bodies, or account identities.
type QuotaIssue struct {
	Section    string `json:"section"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
}

// QuotaQueryError preserves status and cancellation without exposing raw upstream
// responses. Authentication failures and invalid required responses are fatal.
type QuotaQueryError struct {
	Provider   Provider
	Section    string
	HTTPStatus int
	Err        error
}

func (e *QuotaQueryError) Error() string {
	if e.HTTPStatus != 0 {
		return fmt.Sprintf("%s quota %s: HTTP %d", e.Provider, e.Section, e.HTTPStatus)
	}
	return fmt.Sprintf("%s quota %s: %v", e.Provider, e.Section, e.Err)
}

func (e *QuotaQueryError) Unwrap() error { return e.Err }

// SupportsQuotaQuery checks the provider registration without making a request.
func SupportsQuotaQuery(provider Provider) bool {
	module := resolveProviderModule(provider)
	return module != nil && module.Quota != nil
}

// QueryQuota dispatches through the provider's optional account query interface.
// It does not refresh models, retry, cache, or make model-generation requests.
// Callers can set a deadline; otherwise the complete query is bounded to 15s.
func QueryQuota(ctx context.Context, provider Provider, options QuotaQueryOptions) (QuotaResult, error) {
	module := resolveProviderModule(provider)
	if module == nil || module.Quota == nil {
		return QuotaResult{}, fmt.Errorf("%w: %s", ErrQuotaUnsupported, provider)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return QuotaResult{}, err
	}
	result, err := module.Quota.QueryQuota(ctx, options)
	result.Provider = provider
	result.QueriedAt = time.Now().UTC()
	return result, err
}
