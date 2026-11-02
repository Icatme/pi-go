package pigo_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icatme/pi-go/pkg/pigo"
)

func TestKiroIDCRefreshRetentionOptInOnlySuccessfulOmission(t *testing.T) {
	for _, mode := range []string{"strict", "no-options", "omitted", "rotated", "same-returned", "null", "empty", "duplicate", "missing-expiry", "close", "transport", "expired"} {
		t.Run(mode, func(t *testing.T) {
			config := kiroPublicIDCConfig()
			original := config.RefreshToken
			raw := `{"accessToken":"NEW_ACCESS","expiresIn":3600}`
			switch mode {
			case "rotated":
				raw = `{"accessToken":"NEW_ACCESS","refreshToken":"NEW_REFRESH","expiresIn":3600}`
			case "same-returned":
				raw = `{"accessToken":"NEW_ACCESS","refreshToken":"PRIVATE_OLD","expiresIn":3600}`
			case "null":
				raw = `{"accessToken":"NEW_ACCESS","refreshToken":null,"expiresIn":3600}`
			case "empty":
				raw = `{"accessToken":"NEW_ACCESS","refreshToken":"","expiresIn":3600}`
			case "duplicate":
				raw = `{"accessToken":"NEW_ACCESS","refreshToken":"A","refreshToken":"B","expiresIn":3600}`
			case "missing-expiry":
				raw = `{"accessToken":"NEW_ACCESS"}`
			}
			body := &kiroPublicCatalogBody{Reader: strings.NewReader(raw)}
			if mode == "close" {
				body.failure = errors.New("PRIVATE_CLOSE")
			}
			if mode == "expired" {
				oldClock := config.Clock
				clocks := 0
				config.Clock = func(ctx context.Context) (time.Time, error) {
					clocks++
					stamp, err := oldClock(ctx)
					if clocks == 2 {
						stamp = stamp.Add(time.Hour)
					}
					return stamp, err
				}
			}
			calls := 0
			config.RoundTrip = func(*http.Request) (*http.Response, error) {
				calls++
				response := kiroPublicIDCResponse(body)
				if mode == "transport" {
					return response, errors.New("PRIVATE_TRANSPORT")
				}
				return response, nil
			}
			var result *pigo.KiroIDCRefreshResult
			var err error
			switch mode {
			case "strict":
				result, err = pigo.RefreshKiroIDC(context.Background(), config)
			case "no-options":
				result, err = pigo.RefreshKiroIDCWithOptions(context.Background(), config)
			default:
				result, err = pigo.RefreshKiroIDCWithOptions(context.Background(), config, pigo.WithKiroIDCRetainMissingRefreshToken())
			}
			success := mode == "omitted" || mode == "rotated" || mode == "same-returned"
			if calls != 1 || body.closes != 1 || config.RefreshToken != original {
				t.Fatal("lifecycle or mutation")
			}
			if success {
				expected := original
				if mode == "rotated" {
					expected = "NEW_REFRESH"
				}
				if err != nil || result == nil || result.RefreshToken() != expected || result.RetainedRefreshToken() != (mode == "omitted") || result.Credential().Token != "NEW_ACCESS" {
					t.Fatal("successful disposition", err)
				}
			} else {
				var failure *pigo.KiroIDCRefreshError
				if result != nil || !errors.As(err, &failure) || !failure.ExecutorInvoked() {
					t.Fatal("retention rescued failure", err)
				}
			}
		})
	}
}

func TestKiroIDCRefreshRetentionInvalidOptionsBeforeCallbacks(t *testing.T) {
	for _, options := range [][]pigo.KiroIDCRefreshOption{{{}}, {pigo.WithKiroIDCRetainMissingRefreshToken(), pigo.WithKiroIDCRetainMissingRefreshToken()}} {
		config := kiroPublicIDCConfig()
		calls, clocks := 0, 0
		config.Clock = func(context.Context) (time.Time, error) { clocks++; return time.Time{}, nil }
		config.RoundTrip = func(*http.Request) (*http.Response, error) { calls++; return nil, nil }
		result, err := pigo.RefreshKiroIDCWithOptions(context.Background(), config, options...)
		var failure *pigo.KiroIDCRefreshError
		if result != nil || !errors.As(err, &failure) || failure.ExecutorInvoked() || calls != 0 || clocks != 0 {
			t.Fatal("invalid option side effects")
		}
	}
}

func TestKiroIDCRefreshRetentionConcurrentIndependentInputs(t *testing.T) {
	// This verifies no library-global token cache. It DOES NOT authorize parallel
	// refresh of the same real token or provide source-generation locking.
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			config := kiroPublicIDCConfig()
			config.RefreshToken = fmt.Sprintf("LOCAL_TOKEN_%d", i)
			original := config.RefreshToken
			config.RoundTrip = func(*http.Request) (*http.Response, error) {
				return kiroPublicIDCResponse(&kiroPublicCatalogBody{Reader: strings.NewReader(`{"accessToken":"NEW_ACCESS","expiresIn":3600}`)}), nil
			}
			result, err := pigo.RefreshKiroIDCWithOptions(context.Background(), config, pigo.WithKiroIDCRetainMissingRefreshToken())
			if err != nil || result == nil || result.RefreshToken() != original || !result.RetainedRefreshToken() {
				t.Error("cross-attempt token mix", err)
			}
		}(i)
	}
	wg.Wait()
}
