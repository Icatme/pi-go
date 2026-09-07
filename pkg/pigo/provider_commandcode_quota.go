package pigo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Command Code's account API is the alpha API used by its CLI /usage command.
// Keep its wire types separate from the public, provider-neutral quota result.
type commandCodeQuotaQuerier struct{}

type commandCodeQuotaClient struct {
	client *http.Client
	base   string
	key    string
	orgID  string
}

func (commandCodeQuotaQuerier) QueryQuota(ctx context.Context, options QuotaQueryOptions) (QuotaResult, error) {
	key := usableCommandCodeAPIKey(options.APIKey)
	if strings.TrimSpace(options.APIKey) == "" {
		if _, supplied := options.Auth["commandcode"]; supplied {
			key = usableCommandCodeAPIKey(ResolveAPIKey("commandcode", options.Auth))
		} else {
			key = ResolveCommandCodeAPIKey(nil)
		}
	}
	if key == "" {
		return QuotaResult{}, ErrQuotaCredentials
	}
	base := strings.TrimSpace(options.BaseURL)
	if base == "" {
		base = resolveCommandCodeAPIBaseURL()
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return QuotaResult{}, commandCodeQuotaError("configuration", 0, errors.New("invalid account API base URL"))
	}
	client := options.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	// Never send account credentials through redirects, including redirects to
	// another path on the same origin. Preserve the caller's client unchanged.
	queryClient := *client
	queryClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	api := commandCodeQuotaClient{client: &queryClient, base: strings.TrimRight(base, "/"), key: key}

	var account struct {
		Org  json.RawMessage `json:"org"`
		User json.RawMessage `json:"user"`
	}
	if err := api.get(ctx, "/alpha/whoami", "account", &account); err != nil {
		return QuotaResult{}, err
	}
	if len(account.Org) > 0 && string(account.Org) != "null" {
		var org struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(account.Org, &org) != nil || strings.TrimSpace(org.ID) == "" {
			return QuotaResult{}, commandCodeQuotaError("account", 0, errors.New("unrecognized organization response"))
		}
		api.orgID = org.ID
	} else {
		var user struct {
			ID       string `json:"id"`
			UserName string `json:"userName"`
			Name     string `json:"name"`
		}
		if json.Unmarshal(account.User, &user) != nil || strings.TrimSpace(user.ID+user.UserName+user.Name) == "" {
			return QuotaResult{}, commandCodeQuotaError("account", 0, errors.New("unrecognized account response"))
		}
	}

	var wire struct {
		Credits      json.RawMessage `json:"credits"`
		WindowLimits json.RawMessage `json:"windowLimits"`
	}
	if err := api.get(ctx, "/alpha/billing/credits", "credits", &wire); err != nil {
		return QuotaResult{}, err
	}
	var credits struct {
		Monthly   *float64 `json:"monthlyCredits"`
		Purchased *float64 `json:"purchasedCredits"`
		Free      *float64 `json:"freeCredits"`
	}
	if json.Unmarshal(wire.Credits, &credits) != nil || !commandCodeQuotaNumber(credits.Monthly) || !commandCodeQuotaNumber(credits.Purchased) || !commandCodeQuotaNumber(credits.Free) {
		return QuotaResult{}, commandCodeQuotaError("credits", 0, errors.New("incomplete or invalid credit pools"))
	}
	// The CLI clamps each credit pool separately. Plan credits can be below
	// zero without consuming purchased credit. Preserve the pools because only
	// monthly credits are constrained by the subscription's rolling windows.
	result := QuotaResult{Balances: []QuotaBalance{}, Windows: []QuotaWindow{}}
	for _, pool := range []struct {
		id     string
		amount float64
	}{{"monthly", *credits.Monthly}, {"purchased", *credits.Purchased}, {"free", *credits.Free}} {
		remaining := max(0, pool.amount)
		result.Balances = append(result.Balances, QuotaBalance{ID: pool.id, Unit: "credits", Remaining: &remaining})
	}
	commandCodeQuotaWindows(wire.WindowLimits, &result)

	var subscription struct {
		Data json.RawMessage `json:"data"`
	}
	err = api.get(ctx, "/alpha/billing/subscriptions", "subscription", &subscription)
	if err == nil && string(subscription.Data) != "null" {
		result.Subscription, err = commandCodeQuotaSubscription(subscription.Data)
	}
	if err != nil {
		var queryErr *QuotaQueryError
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &queryErr) && (queryErr.HTTPStatus == 401 || queryErr.HTTPStatus == 403)) {
			return QuotaResult{}, err
		}
		issue := QuotaIssue{Section: "subscription", Message: err.Error()}
		if queryErr != nil {
			issue.HTTPStatus = queryErr.HTTPStatus
		}
		result.Unavailable = append(result.Unavailable, issue)
	}
	return result, nil
}

func commandCodeQuotaError(section string, status int, err error) *QuotaQueryError {
	return &QuotaQueryError{Provider: "commandcode", Section: section, HTTPStatus: status, Err: err}
}

func (c commandCodeQuotaClient) get(ctx context.Context, path, section string, target any) error {
	endpoint := c.base + path
	if c.orgID != "" {
		endpoint += "?" + url.Values{"orgId": {c.orgID}}.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return commandCodeQuotaError(section, 0, errors.New("could not create request"))
	}
	request.Header.Set("Authorization", "Bearer "+c.key)
	request.Header.Set("Accept", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return commandCodeQuotaError(section, 0, ctx.Err())
		}
		return commandCodeQuotaError(section, 0, errors.New("account API request failed"))
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return commandCodeQuotaError(section, response.StatusCode, errors.New("account API rejected request"))
	}
	const maxResponse = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil {
		if ctx.Err() != nil {
			return commandCodeQuotaError(section, 0, ctx.Err())
		}
		return commandCodeQuotaError(section, 0, errors.New("could not read account response"))
	}
	if len(body) > maxResponse {
		return commandCodeQuotaError(section, 0, errors.New("account response exceeds size limit"))
	}
	if json.Unmarshal(body, target) != nil {
		return commandCodeQuotaError(section, 0, errors.New("invalid account JSON response"))
	}
	return nil
}

func commandCodeQuotaNumber(value *float64) bool {
	return value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0)
}

func commandCodeQuotaWindows(raw json.RawMessage, result *QuotaResult) {
	var limits struct {
		Limited  *bool           `json:"limited"`
		FiveHour json.RawMessage `json:"fiveHour"`
		Weekly   json.RawMessage `json:"weekly"`
	}
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &limits) != nil {
		result.Unavailable = append(result.Unavailable, QuotaIssue{Section: "windows", Message: "window limits unavailable"})
		return
	}
	if limits.Limited != nil && !*limits.Limited {
		return
	}
	for _, entry := range []struct {
		id  string
		raw json.RawMessage
	}{{"fiveHour", limits.FiveHour}, {"weekly", limits.Weekly}} {
		var wire struct {
			Used    *float64 `json:"used"`
			Cap     *float64 `json:"cap"`
			ResetAt *int64   `json:"resetAt"`
		}
		if json.Unmarshal(entry.raw, &wire) != nil || !commandCodeQuotaNumber(wire.Used) || !commandCodeQuotaNumber(wire.Cap) || *wire.Used < 0 || *wire.Cap < 0 || (wire.ResetAt != nil && *wire.ResetAt < 0) {
			result.Unavailable = append(result.Unavailable, QuotaIssue{Section: "windows." + entry.id, Message: "window limit unavailable or invalid"})
			continue
		}
		remaining := max(0, *wire.Cap-*wire.Used)
		window := QuotaWindow{ID: entry.id, Unit: "credits", BalanceIDs: []string{"monthly"}, Limit: wire.Cap, Used: wire.Used, Remaining: &remaining}
		// The account API returns Unix milliseconds. Zero means no active reset.
		if wire.ResetAt != nil && *wire.ResetAt > 0 {
			reset := time.UnixMilli(*wire.ResetAt).UTC()
			if reset.Year() > 9999 {
				result.Unavailable = append(result.Unavailable, QuotaIssue{Section: "windows." + entry.id + ".resetsAt", Message: "invalid window reset time"})
			} else {
				window.ResetsAt = &reset
			}
		}
		result.Windows = append(result.Windows, window)
	}
}

func commandCodeQuotaSubscription(raw json.RawMessage) (*QuotaSubscription, error) {
	var wire struct {
		PlanID string `json:"planId"`
		Status string `json:"status"`
		Start  string `json:"currentPeriodStart"`
		End    string `json:"currentPeriodEnd"`
	}
	if json.Unmarshal(raw, &wire) != nil || strings.TrimSpace(wire.PlanID+wire.Status) == "" {
		return nil, commandCodeQuotaError("subscription", 0, errors.New("unrecognized subscription response"))
	}
	result := &QuotaSubscription{Plan: wire.PlanID, Status: wire.Status}
	for _, field := range []struct {
		raw    string
		target **time.Time
	}{{wire.Start, &result.PeriodStart}, {wire.End, &result.PeriodEnd}} {
		if field.raw == "" {
			continue
		}
		value, err := time.Parse(time.RFC3339Nano, field.raw)
		if err != nil {
			return nil, commandCodeQuotaError("subscription", 0, errors.New("invalid subscription date"))
		}
		value = value.UTC()
		*field.target = &value
	}
	return result, nil
}
