package pigo_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icatme/pi-go/pkg/pigo"
)

type kiroPublicCatalogBody struct {
	io.Reader
	closes  int
	failure error
}

func (b *kiroPublicCatalogBody) Close() error { b.closes++; return b.failure }

func TestKiroPublicCatalogFetchImmutableObservationNoRegistration(t *testing.T) {
	scope := pigo.KiroCredentialScope{Region: "us-east-1", ProfileARN: "PRIVATE_PROFILE"}
	now := time.Date(2026, 11, 2, 5, 0, 0, 0, time.UTC)
	raw := []byte(`{"models":[{"modelId":"sample-2.3","displayName":"Example","tokenLimits":{"maxInputTokens":9007199254740993},"additionalModelRequestFieldsSchema":{"type":"object"},"privateNote":"PRIVATE_METADATA"},{"modelId":"other"}]}`)
	body := &kiroPublicCatalogBody{Reader: bytes.NewReader(raw)}
	sources, calls, clocks := 0, 0, 0
	config := pigo.KiroCatalogFetchConfig{Scope: scope, Origin: "LOCAL_TEST", UserAgent: "local/1", MaxBytes: 4096, MaxModels: 8,
		Credentials: func(_ context.Context, received pigo.KiroCredentialScope) (pigo.KiroScopedCredential, error) {
			sources++
			if received != scope {
				t.Error("scope")
			}
			return pigo.KiroScopedCredential{Scope: received, Kind: pigo.KiroCredentialBearer, Token: "LOCAL_PUBLIC_CATALOG"}, nil
		},
		RoundTrip: func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Method != http.MethodGet || r.URL.Path != "/List-Available-Models" || r.Header.Get("Authorization") != "Bearer LOCAL_PUBLIC_CATALOG" {
				t.Error("public request")
			}
			return &http.Response{StatusCode: 200, ContentLength: int64(len(raw)), Header: http.Header{"Content-Type": []string{"application/json"}}, Body: body}, nil
		},
		Clock: func(context.Context) (time.Time, error) {
			clocks++
			if body.closes != 1 {
				t.Error("clock before Close")
			}
			return now, nil
		}}
	before := pigo.GetProviders()
	sort.Slice(before, func(i, j int) bool { return before[i] < before[j] })
	catalog, err := pigo.FetchKiroModelCatalog(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	after := pigo.GetProviders()
	sort.Slice(after, func(i, j int) bool { return after[i] < after[j] })
	if sources != 1 || calls != 1 || clocks != 1 || body.closes != 1 || !reflect.DeepEqual(before, after) {
		t.Fatal("side effects or automatic registration")
	}
	models, err := catalog.Models(scope, now, time.Hour)
	if err != nil || len(models) != 2 || models[0].Alias() != "sample-2-3" || models[0].ServiceID() != "sample-2.3" || models[0].DisplayName() != "Example" {
		t.Fatal("public observations", err)
	}
	if value, present := models[0].MaxInputTokens(); !present || value != 9007199254740993 {
		t.Fatal("integer evidence")
	}
	if _, present := models[0].MaxOutputTokens(); present {
		t.Fatal("invented output limit")
	}
	if _, present := models[1].MaxInputTokens(); present {
		t.Fatal("invented input limit")
	}
	metadata := models[0].RawMetadata()
	schema := models[0].RequestFieldsSchema()
	metadata[0] = 'x'
	schema[0] = 'x'
	models[0] = pigo.KiroModelObservation{}
	selected, err := catalog.Select(scope, pigo.KiroCatalogServiceID, "sample-2.3", now, time.Hour)
	if err != nil || selected.RawMetadata()[0] != '{' || selected.RequestFieldsSchema()[0] != '{' {
		t.Fatal("caller modified snapshot")
	}
	for _, value := range []any{catalog, *catalog, selected, config} {
		encoded, _ := json.Marshal(value)
		for _, s := range []string{string(encoded), fmt.Sprintf("%v", value), fmt.Sprintf("%+v", value), fmt.Sprintf("%#v", value)} {
			if strings.Contains(s, "PRIVATE_") || strings.Contains(s, "LOCAL_PUBLIC_CATALOG") {
				t.Fatal("automatic diagnostic disclosure")
			}
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			list, err := catalog.Models(scope, now, time.Hour)
			if err != nil || len(list) != 2 {
				t.Error("concurrent list")
				return
			}
			list[0].RawMetadata()[0] = 'x'
		}()
	}
	wg.Wait()
	for _, mode := range []string{"profile", "stale", "unknown-kind", "unknown-id"} {
		requested := scope
		clock := now
		kind := pigo.KiroCatalogAlias
		id := "sample-2-3"
		switch mode {
		case "profile":
			requested.ProfileARN = "other"
		case "stale":
			clock = now.Add(time.Hour)
		case "unknown-kind":
			kind = "guess"
		case "unknown-id":
			id = "default"
		}
		if _, err := catalog.Select(requested, kind, id, clock, time.Hour); err == nil {
			t.Fatal("unsafe selection", mode)
		}
		if mode == "profile" || mode == "stale" {
			if list, err := catalog.Models(requested, clock, time.Hour); err == nil || list != nil {
				t.Fatal("unsafe listing", mode)
			}
		}
	}
}

func TestKiroPublicCatalogZeroConfigAndLateFailure(t *testing.T) {
	if catalog, err := pigo.FetchKiroModelCatalog(context.Background(), pigo.KiroCatalogFetchConfig{}); err == nil || catalog != nil {
		t.Fatal("implicit defaults")
	}
	for _, catalog := range []*pigo.KiroModelCatalog{nil, {}} {
		if list, err := catalog.Models(pigo.KiroCredentialScope{}, time.Now(), time.Hour); err == nil || list != nil {
			t.Fatal("zero catalog list")
		}
		if _, err := catalog.Select(pigo.KiroCredentialScope{}, pigo.KiroCatalogAlias, "default", time.Now(), time.Hour); err == nil {
			t.Fatal("zero catalog selection")
		}
	}
	scope := pigo.KiroCredentialScope{Region: "us-east-1", ProfileARN: "profile"}
	body := &kiroPublicCatalogBody{Reader: strings.NewReader(`{"models":[{"modelId":"sample"}]}`), failure: errors.New("PRIVATE_CLOSE")}
	clocks := 0
	config := pigo.KiroCatalogFetchConfig{Scope: scope, Origin: "LOCAL_TEST", UserAgent: "local/1", MaxBytes: 4096, MaxModels: 8,
		Credentials: func(_ context.Context, s pigo.KiroCredentialScope) (pigo.KiroScopedCredential, error) {
			return pigo.KiroScopedCredential{Scope: s, Kind: pigo.KiroCredentialBearer, Token: "LOCAL_ONLY"}, nil
		},
		RoundTrip: func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, ContentLength: -1, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: body}, nil
		}, Clock: func(context.Context) (time.Time, error) { clocks++; return time.Now(), nil }}
	catalog, err := pigo.FetchKiroModelCatalog(context.Background(), config)
	if err == nil || catalog != nil || body.closes != 1 || clocks != 0 || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatal("late failure escaped")
	}
}

func TestKiroPublicCatalogCallbackErrorsRedacted(t *testing.T) {
	for _, mode := range []string{"source", "executor", "clock"} {
		t.Run(mode, func(t *testing.T) {
			scope := pigo.KiroCredentialScope{Region: "us-east-1", ProfileARN: "profile"}
			calls, clocks := 0, 0
			body := &kiroPublicCatalogBody{Reader: strings.NewReader(`{"models":[{"modelId":"sample"}]}`)}
			config := pigo.KiroCatalogFetchConfig{Scope: scope, Origin: "LOCAL_TEST", UserAgent: "local/1", MaxBytes: 4096, MaxModels: 8,
				Credentials: func(_ context.Context, s pigo.KiroCredentialScope) (pigo.KiroScopedCredential, error) {
					if mode == "source" {
						return pigo.KiroScopedCredential{}, errors.New("PRIVATE_SOURCE")
					}
					return pigo.KiroScopedCredential{Scope: s, Kind: pigo.KiroCredentialBearer, Token: "LOCAL_ONLY"}, nil
				},
				RoundTrip: func(*http.Request) (*http.Response, error) {
					calls++
					response := &http.Response{StatusCode: 200, ContentLength: -1, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: body}
					if mode == "executor" {
						return response, errors.New("PRIVATE_EXECUTOR")
					}
					return response, nil
				}, Clock: func(context.Context) (time.Time, error) { clocks++; return time.Time{}, errors.New("PRIVATE_CLOCK") }}
			catalog, err := pigo.FetchKiroModelCatalog(context.Background(), config)
			if catalog != nil || err == nil || err.Error() != "kiro: explicit catalog observation failed" {
				t.Fatal("callback error not redacted")
			}
			if mode == "source" {
				if calls != 0 || body.closes != 0 || clocks != 0 {
					t.Fatal("source failure side effects")
				}
			} else if calls != 1 || body.closes != 1 || (mode == "executor" && clocks != 0) || (mode == "clock" && clocks != 1) {
				t.Fatal("callback lifecycle")
			}
		})
	}
}
