package codemode

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSearchDescriptionBudget(t *testing.T) {
	catalog := []ToolSummary{{Name: "a", Description: "猫😀needle", Namespace: "allowed"}, {Name: "b", Description: "needle long text", Namespace: "allowed"}, {Name: "hidden", Description: "needle", Namespace: "other"}}
	original := append([]ToolSummary(nil), catalog...)
	opts := SearchOptions{Namespace: "allowed", Limit: 2}
	baseline, e := SearchTools(catalog, "needle", opts)
	if e != nil || len(baseline) != 2 {
		t.Fatal(baseline, e)
	}
	raw, _ := json.Marshal(baseline)
	if strings.Contains(string(raw), "descriptionTruncated") {
		t.Fatal(string(raw))
	}
	for _, b := range []SearchDescriptionBudget{{0, 0}, {1, 100}, {3, 4}, {6, 8}, {100, 0}, {100, 100}, {int(^uint(0) >> 1), int(^uint(0) >> 1)}} {
		opts.DescriptionBudget = &b
		got, e := SearchTools(catalog, "needle", opts)
		if e != nil {
			t.Fatal(e)
		}
		remaining := b.MaxTotalBytes
		for i, m := range got {
			want := baseline[i]
			n := min(len(want.Description), b.MaxPerMatchBytes, remaining)
			for n > 0 && !utf8.ValidString(want.Description[:n]) {
				n--
			}
			remaining -= n
			if m.Name != want.Name || m.Namespace != want.Namespace || m.Score != want.Score || m.Description != want.Description[:n] || m.DescriptionTruncated != (n < len(want.Description)) {
				t.Fatalf("budget%+v got%+v baseline%+v", b, got, baseline)
			}
		}
	}
	if !reflect.DeepEqual(catalog, original) {
		t.Fatal("catalog changed")
	}
	again, _ := SearchTools(catalog, "needle", SearchOptions{Namespace: "allowed", Limit: 2})
	if !reflect.DeepEqual(again, baseline) {
		t.Fatal("subsequent legacy call changed")
	}
	for _, b := range []SearchDescriptionBudget{{-1, 1}, {1, -1}} {
		if _, e := SearchTools(nil, "", SearchOptions{DescriptionBudget: &b}); e == nil {
			t.Fatal("negative accepted")
		}
	}
	empty, e := SearchTools([]ToolSummary{{Name: "needle"}}, "needle", SearchOptions{DescriptionBudget: &SearchDescriptionBudget{}})
	if e != nil || len(empty) != 1 || empty[0].DescriptionTruncated {
		t.Fatal(empty, e)
	}
}

func TestSearchDescriptionBudgetSandbox(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	full := "猫😀needle full description"
	tool := Tool{Name: "needle", Description: full, Invoke: func(context.Context, HostCall) (json.RawMessage, error) {
		t.Fatal("discovery executed")
		return nil, nil
	}}
	result, e := s.Run(t.Context(), `const a=await searchTools("needle",{descriptionBudget:{maxPerMatchBytes:4,maxTotalBytes:4}}); const b=await describeTool("needle"); const c=await searchTools("needle"); text(JSON.stringify({a,b,c}));`, RunOptions{Tools: []Tool{tool}})
	if e != nil {
		t.Fatal(e)
	}
	var wire struct {
		A []SearchMatch
		B struct{ Description string }
		C []SearchMatch
	}
	if e = json.Unmarshal([]byte(textOutput(result)), &wire); e != nil {
		t.Fatal(textOutput(result), e)
	}
	if len(wire.A) != 1 || wire.A[0].Description != "猫" || !wire.A[0].DescriptionTruncated || wire.B.Description != full || len(wire.C) != 1 || wire.C[0].Description != full || wire.C[0].DescriptionTruncated {
		t.Fatalf("%+v", wire)
	}
	for _, raw := range []string{`null`, `{}`, `{"maxPerMatchBytes":1}`, `{"maxPerMatchBytes":null,"maxTotalBytes":1}`, `{"maxPerMatchBytes":1,"maxTotalBytes":-1}`, `{"maxPerMatchBytes":1.5,"maxTotalBytes":1}`, `{"maxPerMatchBytes":"1","maxTotalBytes":1}`, `{"maxPerMatchBytes":1,"SECRET":1}`, `{"maxPerMatchBytes":1,"maxTotalBytes":1,"extra":1}`, `{"maxPerMatchBytes":1,"maxTotalBytes":1e99}`} {
		_, e := s.Run(t.Context(), `await searchTools("needle",{descriptionBudget:`+raw+`});`, RunOptions{Tools: []Tool{tool}})
		if e == nil || strings.Contains(e.Error(), "SECRET") {
			t.Fatalf("bad budget %s: %v", raw, e)
		}
	}
}

func TestSearchDescriptionBudgetRemainder(t *testing.T) {
	c := []ToolSummary{{Name: "needle_a", Description: "😀"}, {Name: "needle_b", Description: "a"}}
	got, e := SearchTools(c, "needle", SearchOptions{DescriptionBudget: &SearchDescriptionBudget{MaxPerMatchBytes: 3, MaxTotalBytes: 3}})
	if e != nil || len(got) != 2 || got[0].Name != "needle_a" || got[0].Description != "" || !got[0].DescriptionTruncated || got[1].Description != "a" || got[1].DescriptionTruncated {
		t.Fatalf("%+v %v", got, e)
	}
}
