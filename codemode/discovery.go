package codemode

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type toolDescription struct {
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	Namespace         string          `json:"namespace,omitempty"`
	ResultDescription string          `json:"resultDescription,omitempty"`
	Parameters        json.RawMessage `json:"parameters"`
	OutputSchema      json.RawMessage `json:"outputSchema,omitempty"`
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
		if t.Name == "" || len(t.Name) > 128 || len(t.Namespace) > 128 || len(t.Description) > 32<<10 || len(t.ResultDescription) > 4096 || t.Invoke == nil || names[t.Name] || !utf8.ValidString(t.Name+t.Namespace+t.Description+t.ResultDescription) {
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
		bytes += len(t.Name) + len(t.Namespace) + len(t.Description) + len(t.ResultDescription) + len(t.Parameters) + len(t.OutputSchema)
		if bytes > maxBytes {
			return nil, nil, fmt.Errorf("tool catalog exceeds byte limit")
		}
		t.Parameters = append(json.RawMessage(nil), t.Parameters...)
		t.OutputSchema = append(json.RawMessage(nil), t.OutputSchema...)
		copyOf[i] = t
		descriptions[i] = toolDescription{Name: t.Name, Description: t.Description, Namespace: t.Namespace, ResultDescription: t.ResultDescription, Parameters: t.Parameters, OutputSchema: t.OutputSchema}
	}
	return copyOf, descriptions, nil
}

func copyNamespaces(values []Namespace, catalog []toolDescription, maxBytes int) ([]Namespace, error) {
	if len(values) > 256 {
		return nil, fmt.Errorf("too many visible namespaces")
	}
	visible := make(map[string]bool)
	for _, tool := range catalog {
		if tool.Namespace != "" {
			visible[tool.Namespace] = true
		}
	}
	seen := make(map[string]bool)
	bytes := 0
	for _, value := range values {
		if value.Name == "" || len(value.Name) > 128 || !visible[value.Name] || seen[value.Name] || len(value.Description) > 4096 || len(value.Instructions) > 32<<10 || !utf8.ValidString(value.Name+value.Description+value.Instructions) {
			return nil, fmt.Errorf("invalid or unavailable namespace metadata")
		}
		seen[value.Name] = true
		bytes += len(value.Name) + len(value.Description) + len(value.Instructions)
		if bytes > maxBytes {
			return nil, fmt.Errorf("namespace catalog exceeds byte limit")
		}
	}
	return append([]Namespace(nil), values...), nil
}

type searchRequest struct {
	Query   string                     `json:"query"`
	Options map[string]json.RawMessage `json:"options"`
}

// ToolSummary is bounded searchable metadata. It contains no schema or executor.
type ToolSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Namespace   string `json:"namespace,omitempty"`
}

type SearchOptions struct {
	Namespace string
	Limit     int
}

type SearchMatch struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Namespace   string  `json:"namespace,omitempty"`
	Score       float64 `json:"score"`
}

func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func searchTools(catalog []toolDescription, raw string) ([]SearchMatch, error) {
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
	namespace, filterNamespace := "", false
	for k, v := range req.Options {
		switch k {
		case "limit":
			if err := json.Unmarshal(v, &limit); err != nil || limit < 1 || limit > 50 {
				return nil, fmt.Errorf("searchTools limit must be an integer in 1..50")
			}
		case "namespace":
			if err := json.Unmarshal(v, &namespace); err != nil || len(namespace) > 128 {
				return nil, fmt.Errorf("searchTools namespace must be a string of at most 128 bytes")
			}
			filterNamespace = true
		default:
			return nil, fmt.Errorf("searchTools options support only namespace and limit")
		}
	}
	if filterNamespace {
		filtered := make([]toolDescription, 0, len(catalog))
		for _, tool := range catalog {
			if tool.Namespace == namespace {
				filtered = append(filtered, tool)
			}
		}
		catalog = filtered
	}
	summaries := make([]ToolSummary, len(catalog))
	for i, tool := range catalog {
		summaries[i] = ToolSummary{Name: tool.Name, Description: tool.Description, Namespace: tool.Namespace}
	}
	return SearchTools(summaries, req.Query, SearchOptions{Limit: limit})
}

// SearchTools performs the same BM25 search used inside the sandbox. Callers
// must supply only authorized summaries; hidden names are never inferred.
func SearchTools(catalog []ToolSummary, query string, options SearchOptions) ([]SearchMatch, error) {
	if len(catalog) > 256 || len(query) > 4096 || !utf8.ValidString(query) || len(options.Namespace) > 128 || !utf8.ValidString(options.Namespace) {
		return nil, fmt.Errorf("searchTools catalog or query exceeds metadata limits")
	}
	limit := options.Limit
	if limit == 0 {
		limit = 5
	}
	if limit < 1 || limit > 50 {
		return nil, fmt.Errorf("searchTools limit must be an integer in 1..50")
	}
	filtered := make([]ToolSummary, 0, len(catalog))
	for _, tool := range catalog {
		if tool.Name == "" || len(tool.Name) > 128 || len(tool.Namespace) > 128 || len(tool.Description) > 32<<10 || !utf8.ValidString(tool.Name+tool.Namespace+tool.Description) {
			return nil, fmt.Errorf("searchTools catalog exceeds metadata limits")
		}
		if options.Namespace == "" || tool.Namespace == options.Namespace {
			filtered = append(filtered, tool)
		}
	}
	catalog = filtered
	terms := tokenize(query)
	results := make([]SearchMatch, 0)
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
			results = append(results, SearchMatch{Name: t.Name, Description: t.Description, Namespace: t.Namespace, Score: score})
		}
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}
