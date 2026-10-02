package codemode

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
)

type toolDescription struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Namespace    string          `json:"namespace,omitempty"`
	Parameters   json.RawMessage `json:"parameters"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
}

func copyTools(tools []Tool, maxBytes int) ([]Tool, []toolDescription, error) {
	if len(tools) > 256 {
		return nil, nil, fmt.Errorf("too many visible tools")
	}
	copyOf := make([]Tool, len(tools))
	descriptions := make([]toolDescription, len(tools))
	names := map[string]bool{}
	bytes := 0
	for i, t := range tools {
		if t.Name == "" || len(t.Name) > 128 || len(t.Namespace) > 128 || len(t.Description) > 32<<10 || t.Invoke == nil || names[t.Name] {
			return nil, nil, fmt.Errorf("invalid or duplicate tool %q", t.Name)
		}
		names[t.Name] = true
		if len(t.Parameters) == 0 {
			t.Parameters = json.RawMessage(`{}`)
		}
		if len(t.Parameters) > 64<<10 || len(t.OutputSchema) > 64<<10 {
			return nil, nil, fmt.Errorf("tool schema exceeds byte limit")
		}
		if err := ValidateJSON(t.Parameters); err != nil {
			return nil, nil, fmt.Errorf("tool %s schema: %w", t.Name, err)
		}
		if len(t.OutputSchema) > 0 {
			if err := ValidateJSON(t.OutputSchema); err != nil {
				return nil, nil, fmt.Errorf("tool %s output schema: %w", t.Name, err)
			}
		}
		bytes += len(t.Name) + len(t.Namespace) + len(t.Description) + len(t.Parameters) + len(t.OutputSchema)
		if bytes > maxBytes {
			return nil, nil, fmt.Errorf("tool catalog exceeds byte limit")
		}
		t.Parameters = append(json.RawMessage(nil), t.Parameters...)
		t.OutputSchema = append(json.RawMessage(nil), t.OutputSchema...)
		copyOf[i] = t
		descriptions[i] = toolDescription{t.Name, t.Description, t.Namespace, t.Parameters, t.OutputSchema}
	}
	return copyOf, descriptions, nil
}

type searchRequest struct {
	Query   string                     `json:"query"`
	Options map[string]json.RawMessage `json:"options"`
}
type searchMatch struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Score       float64 `json:"score"`
}

func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func searchTools(catalog []toolDescription, raw string) ([]searchMatch, error) {
	if err := ValidateJSON([]byte(raw)); err != nil {
		return nil, err
	}
	var req searchRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return nil, err
	}
	if len(req.Query) > 4096 {
		return nil, fmt.Errorf("search query exceeds byte limit")
	}
	limit := 5
	for k, v := range req.Options {
		if k != "limit" {
			return nil, fmt.Errorf("unknown search option %s", k)
		}
		if err := json.Unmarshal(v, &limit); err != nil || limit < 1 || limit > 50 {
			return nil, fmt.Errorf("search limit must be 1..50")
		}
	}
	terms := tokenize(req.Query)
	results := make([]searchMatch, 0)
	if len(terms) == 0 || len(catalog) == 0 {
		return results, nil
	}
	docs := make([]map[string]int, len(catalog))
	lengths := make([]int, len(catalog))
	df := map[string]int{}
	sum := 0
	for i, t := range catalog {
		docs[i] = map[string]int{}
		for _, w := range tokenize(t.Name + " " + t.Namespace + " " + t.Description) {
			docs[i][w]++
			lengths[i]++
			sum++
		}
		for w := range docs[i] {
			df[w]++
		}
	}
	avg := float64(sum) / float64(len(catalog))
	if avg == 0 {
		return results, nil
	}
	for i, t := range catalog {
		score := 0.0
		for _, term := range terms {
			tf := float64(docs[i][term])
			if tf == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(catalog)-df[term])+0.5)/(float64(df[term])+0.5))
			score += idf * (tf * 2.2) / (tf + 1.2*(0.25+0.75*float64(lengths[i])/avg))
		}
		if score > 0 {
			results = append(results, searchMatch{t.Name, t.Description, score})
		}
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}
