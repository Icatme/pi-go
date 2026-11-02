package pigo

import (
	"bytes"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestKiroOfflineCatalogPreservesEvidence(t *testing.T) {
	raw := []byte(`{"models":[{"modelId":"sample-2.3","displayName":"示例","tokenLimits":{"maxInputTokens":9007199254740993,"maxOutputTokens":99,"future":true},"additionalModelRequestFieldsSchema":{"type":"object","properties":{"effort":{"enum":["low","high"]}},"future":9007199254740993},"unknown":{"n":9007199254740993}},{"modelId":"other","additionalModelRequestFieldsSchema":null}],"futureTop":true}`)
	got, err := parseKiroOfflineModelCatalog(raw, 4096, 5)
	if err != nil || len(got.Models) != 2 {
		t.Fatal("catalog rejected", err)
	}
	first, second := got.Models[0], got.Models[1]
	if first.ID != "sample-2-3" || first.ServiceID != "sample-2.3" || first.DisplayName != "示例" || *first.MaxInputTokens != 9007199254740993 || *first.MaxOutputTokens != 99 {
		t.Fatal("identity or exact limits changed")
	}
	if second.DisplayName != "" || second.MaxInputTokens != nil || second.MaxOutputTokens != nil || second.RequestFieldsSchema != nil {
		t.Fatal("missing metadata guessed")
	}
	if !bytes.Equal(got.Raw, raw) || !bytes.Contains(first.Metadata, []byte(`"unknown"`)) || !bytes.Contains(first.RequestFieldsSchema, []byte("9007199254740993")) {
		t.Fatal("raw metadata lost")
	}
	original := append([]byte(nil), raw...)
	for i := range raw {
		raw[i] = 'x'
	}
	if !bytes.Equal(got.Raw, original) {
		t.Fatal("snapshot aliases source")
	}
	got.Raw[0] = 'x'
	first.Metadata[0] = 'x'
	first.RequestFieldsSchema[0] = 'x'
	*first.MaxInputTokens = 1
	if second.ServiceID != "other" || !bytes.Contains(got.Models[0].Metadata, []byte(`"type":"object"`)) {
		t.Fatal("returned fields alias each other")
	}
}

func TestKiroOfflineCatalogRejectsInvalidOrConflictingModels(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"models":[]}`, `{"models":null}`, `{"models":{}}`, `{"models":[null]}`,
		`{"models":[{"modelId":""}]}`, `{"models":[{"modelId":" x"}]}`,
		`{"models":[{"modelId":"a-1.2"},{"modelId":"a-1-2"}]}`,
		`{"models":[{"modelId":"a"},{"modelId":"a"}]}`,
		`{"models":[{"modelId":"a","modelId":"b"}]}`,
		`{"models":[{"modelId":"a","displayName":false}]}`,
		`{"models":[{"modelId":"x\n"}]}`,
		`{"models":[{"modelId":"a","displayName":" "}]}`,
		`{"models":[{"modelId":"a","displayName":"x\n"}]}`,
		`{"models":[{"modelId":"a","tokenLimits":null}]}`,
		`{"models":[{"modelId":"a","additionalModelRequestFieldsSchema":[]}]}`,
		`{"models":[{"modelId":"a","additionalModelRequestFieldsSchema":{"x":1,"x":2}}]}`,
		`{"models":[{"modelId":"a","additionalModelRequestFieldsSchema":{"x":"\ud800"}}]}`,
	} {
		got, err := parseKiroOfflineModelCatalog([]byte(raw), 4096, 10)
		if err == nil || !reflect.DeepEqual(got, kiroCatalogSnapshot{}) {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestKiroOfflineCatalogLimitsAndIntegerPolicy(t *testing.T) {
	for _, value := range []string{"0", "-1", "1.5", "1e3", "null", "true", `"9"`, "9223372036854775808"} {
		raw := []byte(`{"models":[{"modelId":"valid"},{"modelId":"bad","tokenLimits":{"maxInputTokens":` + value + `}}]}`)
		got, err := parseKiroOfflineModelCatalog(raw, 4096, 10)
		if err == nil || !reflect.DeepEqual(got, kiroCatalogSnapshot{}) {
			t.Fatal("invalid count returned prefix", value)
		}
	}
	raw := []byte(`{"models":[{"modelId":"a","tokenLimits":{"maxOutputTokens":9223372036854775807}}]}`)
	got, err := parseKiroOfflineModelCatalog(raw, len(raw), 1)
	if err != nil || *got.Models[0].MaxOutputTokens != math.MaxInt64 {
		t.Fatal("exact int64 evidence lost", err)
	}
	for _, limits := range [][2]int{{len(raw) - 1, 1}, {0, 1}, {1<<20 + 1, 1}, {4096, 0}, {4096, 1025}} {
		got, err := parseKiroOfflineModelCatalog(raw, limits[0], limits[1])
		if err == nil || !reflect.DeepEqual(got, kiroCatalogSnapshot{}) {
			t.Fatal("budget ignored")
		}
	}
	raw = []byte(`{"models":[{"modelId":"a"},{"modelId":"b"}]}`)
	if _, err := parseKiroOfflineModelCatalog(raw, 4096, 1); err == nil {
		t.Fatal("model count ignored")
	}
	raw = []byte(`{"models":[{"modelId":"` + strings.Repeat("x", 257) + `"}]}`)
	if _, err := parseKiroOfflineModelCatalog(raw, 4096, 1); err == nil {
		t.Fatal("label limit ignored")
	}
}
