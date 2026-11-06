package superagent

import (
	"encoding/json"
	"errors"
)

// Decode exact nested field names; encoding/json otherwise accepts aliases and
// duplicate keys. References contain identities only, never model-authored facts.
func parseReportRefs(d *json.Decoder) ([]HistoryRef, error) {
	invalid := errors.New("pi main router: invalid report references")
	if token, err := d.Token(); err != nil || token != json.Delim('[') {
		return nil, invalid
	}
	refs := []HistoryRef{}
	for d.More() {
		if len(refs) == maxReplyReportRefs {
			return nil, invalid
		}
		if token, err := d.Token(); err != nil || token != json.Delim('{') {
			return nil, invalid
		}
		fields := map[string]string{}
		for d.More() {
			token, err := d.Token()
			key, ok := token.(string)
			if err != nil || !ok || (key != "domain" && key != "kind" && key != "id") {
				return nil, invalid
			}
			if _, ok := fields[key]; ok {
				return nil, invalid
			}
			token, err = d.Token()
			value, ok := token.(string)
			if err != nil || !ok {
				return nil, invalid
			}
			fields[key] = value
		}
		if token, err := d.Token(); err != nil || token != json.Delim('}') || len(fields) != 3 {
			return nil, invalid
		}
		refs = append(refs, HistoryRef{Domain: fields["domain"], Kind: fields["kind"], ID: fields["id"]})
	}
	if token, err := d.Token(); err != nil || token != json.Delim(']') {
		return nil, invalid
	}
	if err := validateReportRefList(refs); err != nil {
		return nil, invalid
	}
	return refs, nil
}
