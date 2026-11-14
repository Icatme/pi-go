package pigo

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const defaultHTTPRetryAfterMaxDelay = 30 * time.Second

// A rejected hint stops this retry without replacing the original HTTP error.
func (c *HTTPStreamClient) waitHTTPRetryDelay(ctx context.Context, headers http.Header, attempt int, baseDelay time.Duration) (bool, error) {
	values := headers.Values("Retry-After")
	if !c.RespectRetryAfter || len(values) == 0 {
		return true, c.waitRetryDelay(ctx, attempt, baseDelay)
	}
	budget := c.RetryAfterMaxDelay
	if budget <= 0 {
		budget = defaultHTTPRetryAfterMaxDelay
	}
	hint, ok := parseHTTPRetryAfter(values, time.Now(), budget)
	if !ok {
		return false, nil
	}
	// Compare before multiplying, so opt-in header paths cannot overflow backoff.
	delay := baseDelay
	for i := 0; i < attempt; i++ {
		if delay > budget/2 {
			delay = budget
			if c.MaxRetryDelay <= 0 || c.MaxRetryDelay > budget {
				return false, nil
			}
			delay = c.MaxRetryDelay
			break
		}
		delay *= 2
	}
	if c.MaxRetryDelay > 0 && delay > c.MaxRetryDelay {
		delay = c.MaxRetryDelay
	}
	if hint > delay {
		delay = hint
	}
	if delay > budget {
		return false, nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-timer.C:
		return true, nil
	}
}

func parseHTTPRetryAfter(values []string, now time.Time, budget time.Duration) (time.Duration, bool) {
	if len(values) != 1 || len(values[0]) > 128 || budget < 0 {
		return 0, false
	}
	value := strings.Trim(values[0], " \t")
	if value == "" {
		return 0, false
	}
	digits := true
	for i := range value {
		if value[i] < '0' || value[i] > '9' {
			digits = false
			break
		}
	}
	if digits {
		n, e := strconv.ParseUint(value, 10, 64)
		if e != nil || n > uint64(budget/time.Second) {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	}
	target, e := http.ParseTime(value)
	if e != nil {
		return 0, false
	}
	if !target.After(now) {
		return 0, true
	}
	if target.After(now.Add(budget)) {
		return 0, false
	}
	return target.Sub(now), true
}
