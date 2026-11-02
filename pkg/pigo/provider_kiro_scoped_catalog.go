package pigo

import (
	"encoding/json"
	"errors"
	"time"
)

var errKiroScopedCatalog = errors.New("kiro: scoped catalog selection rejected")

type kiroCatalogIDKind string

const (
	kiroCatalogAlias     kiroCatalogIDKind = "alias"
	kiroCatalogServiceID kiroCatalogIDKind = "service_id"
)

// Immutable caller-asserted observation, not a cache, model registry or proof of
// entitlement. All state is owned and private; selections return detached copies.
// JSON/fmt does not disclose raw metadata or account/profile labels.
type kiroScopedModelCatalog struct {
	scope      kiroCredentialScope
	observedAt time.Time
	models     []kiroCatalogEntry
	aliases    map[string]int
	services   map[string]int
}

func (kiroScopedModelCatalog) String() string   { return "[kiro scoped catalog]" }
func (kiroScopedModelCatalog) GoString() string { return "[kiro scoped catalog]" }

func newKiroScopedModelCatalog(payload []byte, scope kiroCredentialScope, observedAt time.Time, maxBytes, maxModels int) (*kiroScopedModelCatalog, error) {
	if observedAt.IsZero() || !kiroCatalogLabel(scope.ProfileARN, 2048) || len(scope.Region) > 63 || !kiroRuntimeRegionShape.MatchString(scope.Region) {
		return nil, errKiroScopedCatalog
	}
	snapshot, err := parseKiroOfflineModelCatalog(payload, maxBytes, maxModels)
	if err != nil {
		return nil, errKiroScopedCatalog
	}
	result := &kiroScopedModelCatalog{scope: scope, observedAt: observedAt.UTC(), models: snapshot.Models, aliases: make(map[string]int, len(snapshot.Models)), services: make(map[string]int, len(snapshot.Models))}
	for index, model := range snapshot.Models {
		result.aliases[model.ID] = index
		result.services[model.ServiceID] = index
	}
	return result, nil
}

// selectModel never guesses whether a string is an alias or an exact service ID:
// those namespaces can overlap even when aliases are individually unique. Scope,
// clock and positive maxAge (<=30 days) are explicit caller policy. At maxAge the
// observation is stale; there is no default/fallback/model registration or I/O.
// Observation time is caller asserted, not authenticated management provenance.
func (c *kiroScopedModelCatalog) selectModel(scope kiroCredentialScope, kind kiroCatalogIDKind, id string, now time.Time, maxAge time.Duration) (kiroCatalogEntry, error) {
	zero := kiroCatalogEntry{}
	if c == nil || scope != c.scope || now.IsZero() || now.Before(c.observedAt) || maxAge <= 0 || maxAge > 30*24*time.Hour || now.Sub(c.observedAt) >= maxAge || !kiroCatalogLabel(id, 256) {
		return zero, errKiroScopedCatalog
	}
	var index int
	var ok bool
	switch kind {
	case kiroCatalogAlias:
		index, ok = c.aliases[id]
	case kiroCatalogServiceID:
		index, ok = c.services[id]
	default:
		return zero, errKiroScopedCatalog
	}
	if !ok {
		return zero, errKiroScopedCatalog
	}
	entry := c.models[index]
	if entry.MaxInputTokens != nil {
		value := *entry.MaxInputTokens
		entry.MaxInputTokens = &value
	}
	if entry.MaxOutputTokens != nil {
		value := *entry.MaxOutputTokens
		entry.MaxOutputTokens = &value
	}
	entry.RequestFieldsSchema = append(json.RawMessage(nil), entry.RequestFieldsSchema...)
	entry.Metadata = append(json.RawMessage(nil), entry.Metadata...)
	return entry, nil
}
