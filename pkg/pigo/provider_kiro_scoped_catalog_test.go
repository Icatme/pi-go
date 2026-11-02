package pigo

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestKiroScopedCatalogExplicitNamespacesAndExactServiceIDs(t *testing.T) {
	scope, now := kiroRefreshFixture()
	// Individually unique aliases can overlap a different entry's exact service ID.
	raw := []byte(`{"models":[{"modelId":"sample-1.2.3"},{"modelId":"sample-1-2.3"}]}`)
	catalog, err := newKiroScopedModelCatalog(raw, scope, now, 4096, 8)
	if err != nil {
		t.Fatal(err)
	}
	alias, err := catalog.selectModel(scope, kiroCatalogAlias, "sample-1-2.3", now, time.Hour)
	if err != nil || alias.ServiceID != "sample-1.2.3" {
		t.Fatal("alias authority", err)
	}
	service, err := catalog.selectModel(scope, kiroCatalogServiceID, "sample-1-2.3", now, time.Hour)
	if err != nil || service.ServiceID != "sample-1-2.3" {
		t.Fatal("service authority", err)
	}
	if alias.MaxInputTokens != nil || alias.MaxOutputTokens != nil || alias.DisplayName != "" {
		t.Fatal("guessed model properties")
	}
	if _, err := catalog.selectModel(scope, "guess", "sample-1-2.3", now, time.Hour); err == nil {
		t.Fatal("guessed namespace")
	}
}

func TestKiroScopedCatalogScopeFreshnessAndNoFallback(t *testing.T) {
	scope, now := kiroRefreshFixture()
	catalog, err := newKiroScopedModelCatalog([]byte(`{"models":[{"modelId":"model-2.3"}]}`), scope, now, 4096, 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.selectModel(scope, kiroCatalogAlias, "model-2-3", now.Add(time.Hour-time.Nanosecond), time.Hour); err != nil {
		t.Fatal("fresh boundary")
	}
	for _, mode := range []string{"profile", "region", "future-observation", "stale", "unknown", "zero-age", "long-age", "zero-clock", "nil"} {
		t.Run(mode, func(t *testing.T) {
			input := scope
			clock := now
			age := time.Hour
			id := "model-2-3"
			value := catalog
			switch mode {
			case "profile":
				input.ProfileARN = "other"
			case "region":
				input.Region = "eu-central-1"
			case "future-observation":
				clock = now.Add(-time.Nanosecond)
			case "stale":
				clock = now.Add(time.Hour)
			case "unknown":
				id = "default"
			case "zero-age":
				age = 0
			case "long-age":
				age = 31 * 24 * time.Hour
			case "zero-clock":
				clock = time.Time{}
			case "nil":
				value = nil
			}
			result, err := value.selectModel(input, kiroCatalogAlias, id, clock, age)
			if err != errKiroScopedCatalog || !reflect.DeepEqual(result, kiroCatalogEntry{}) {
				t.Fatal("fallback or scope escape")
			}
		})
	}
}

func TestKiroScopedCatalogOwnershipConcurrentSelectionAndRedaction(t *testing.T) {
	scope, now := kiroRefreshFixture()
	scope.ProfileARN = "PRIVATE_PROFILE"
	raw := []byte(`{"models":[{"modelId":"model-2.3","tokenLimits":{"maxInputTokens":9007199254740993,"maxOutputTokens":100},"additionalModelRequestFieldsSchema":{"type":"object"},"privateNote":"PRIVATE_METADATA"}]}`)
	catalog, err := newKiroScopedModelCatalog(raw, scope, now, 4096, 8)
	if err != nil {
		t.Fatal(err)
	}
	for i := range raw {
		raw[i] = 'x'
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry, err := catalog.selectModel(scope, kiroCatalogAlias, "model-2-3", now, time.Hour)
			if err != nil {
				t.Error(err)
				return
			}
			if *entry.MaxInputTokens != 9007199254740993 || *entry.MaxOutputTokens != 100 || !strings.Contains(string(entry.Metadata), "PRIVATE_METADATA") {
				t.Error("lost evidence")
			}
			*entry.MaxInputTokens = 1
			*entry.MaxOutputTokens = 1
			entry.Metadata[0] = 'x'
			entry.RequestFieldsSchema[0] = 'x'
		}()
	}
	wg.Wait()
	rawJSON, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{string(rawJSON), fmt.Sprintf("%v", catalog), fmt.Sprintf("%+v", catalog), fmt.Sprintf("%#v", catalog), fmt.Sprintf("%#v", *catalog), fmt.Sprintf("%+v", *catalog)} {
		if strings.Contains(s, "PRIVATE_") {
			t.Fatal("catalog diagnostics exposed account/metadata")
		}
	}
}

func TestKiroScopedCatalogConstructorRejectsInvalidEvidence(t *testing.T) {
	scope, now := kiroRefreshFixture()
	for _, raw := range []string{`{"models":[]}`, `{"models":[{"modelId":"m-2.3"},{"modelId":"m-2-3"}]}`, `{"models":[{"modelId":"m","tokenLimits":{"maxInputTokens":1.5}}]}`} {
		if c, err := newKiroScopedModelCatalog([]byte(raw), scope, now, 4096, 8); err != errKiroScopedCatalog || c != nil {
			t.Fatal("invalid catalog accepted")
		}
	}
	if c, err := newKiroScopedModelCatalog([]byte(`{"models":[{"modelId":"m"}]}`), scope, time.Time{}, 4096, 8); err == nil || c != nil {
		t.Fatal("implicit observation time")
	}
	scope.ProfileARN = ""
	if c, err := newKiroScopedModelCatalog([]byte(`{"models":[{"modelId":"m"}]}`), scope, now, 4096, 8); err == nil || c != nil {
		t.Fatal("implicit profile")
	}
}
