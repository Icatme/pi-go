package pigo

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var errKiroModelCatalog = errors.New("kiro: invalid offline model catalog")
var kiroCatalogDecimalID = regexp.MustCompile(`([0-9])\.([0-9])`)

// These are catalog observations, NOT registered Models or account entitlements.
// Missing token limits remain nil; no capabilities, prices or defaults are guessed.
type kiroCatalogEntry struct {
	ID                  string
	ServiceID           string
	DisplayName         string
	MaxInputTokens      *int64
	MaxOutputTokens     *int64
	RequestFieldsSchema json.RawMessage
	Metadata            json.RawMessage
}
type kiroCatalogSnapshot struct {
	Models []kiroCatalogEntry
	Raw    json.RawMessage
}

// parseKiroOfflineModelCatalog accepts caller-supplied JSON only. It does not
// fetch, authenticate, discover profiles, read/write caches or change a registry.
// Inputs must not be mutated concurrently; all returned byte slices are owned.
func parseKiroOfflineModelCatalog(payload []byte, maxBytes, maxModels int) (kiroCatalogSnapshot, error) {
	zero := kiroCatalogSnapshot{}
	if maxBytes < 1 || maxBytes > 1<<20 || maxModels < 1 || maxModels > 1024 {
		return zero, errKiroModelCatalog
	}
	fields, err := decodeKiroJSONObject(payload, maxBytes)
	if err != nil {
		return zero, errKiroModelCatalog
	}
	var models []json.RawMessage
	if json.Unmarshal(fields["models"], &models) != nil || len(models) == 0 || len(models) > maxModels {
		return zero, errKiroModelCatalog
	}
	snapshot := kiroCatalogSnapshot{}
	seen := make(map[string]bool, len(models))
	for _, raw := range models {
		model, err := decodeKiroJSONObject(raw, maxBytes)
		if err != nil {
			return zero, errKiroModelCatalog
		}
		var entry kiroCatalogEntry
		if json.Unmarshal(model["modelId"], &entry.ServiceID) != nil || !kiroCatalogLabel(entry.ServiceID, 256) {
			return zero, errKiroModelCatalog
		}
		entry.ID = kiroCatalogDecimalID.ReplaceAllString(entry.ServiceID, "${1}-${2}")
		if seen[entry.ID] {
			return zero, errKiroModelCatalog
		}
		seen[entry.ID] = true
		if name, ok := model["displayName"]; ok {
			if json.Unmarshal(name, &entry.DisplayName) != nil || !kiroCatalogLabel(entry.DisplayName, 512) {
				return zero, errKiroModelCatalog
			}
		}
		if limits, ok := model["tokenLimits"]; ok {
			values, err := decodeKiroJSONObject(limits, maxBytes)
			if err != nil {
				return zero, errKiroModelCatalog
			}
			for _, field := range []struct {
				name   string
				target **int64
			}{{"maxInputTokens", &entry.MaxInputTokens}, {"maxOutputTokens", &entry.MaxOutputTokens}} {
				if value, ok := values[field.name]; ok {
					// Reject fractions/exponents/coercion. Preserve exact integer evidence.
					literal := bytes.TrimSpace(value)
					for _, digit := range literal {
						if digit < '0' || digit > '9' {
							return zero, errKiroModelCatalog
						}
					}
					count, err := strconv.ParseInt(string(literal), 10, 64)
					if err != nil || count <= 0 {
						return zero, errKiroModelCatalog
					}
					*field.target = &count
				}
			}
		}
		if schema, ok := model["additionalModelRequestFieldsSchema"]; ok && !bytes.Equal(bytes.TrimSpace(schema), []byte("null")) {
			if _, err := decodeKiroJSONObject(schema, maxBytes); err != nil {
				return zero, errKiroModelCatalog
			}
			entry.RequestFieldsSchema = append(json.RawMessage(nil), schema...)
		}
		entry.Metadata = append(json.RawMessage(nil), raw...)
		snapshot.Models = append(snapshot.Models, entry)
	}
	snapshot.Raw = append(json.RawMessage(nil), payload...)
	return snapshot, nil
}

func kiroCatalogLabel(value string, max int) bool {
	return value != "" && len(value) <= max && utf8.ValidString(value) && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}
