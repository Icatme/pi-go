package codemode

import "testing"

func TestSearchToolsExactNamePrecedesShorterDescription(t *testing.T) {
	catalog := []ToolSummary{
		{Name: "mcp__app__queue_read", Description: "Read a queued message of this thread."},
		{Name: "mcp__app__thread_read", Description: "Read durable state and a paginated timeline from any thread. The default view returns user messages, assistant messages and plans. Continue with afterPosition until hasMore is false."},
	}
	got, err := SearchTools(catalog, "mcp__app__thread_read", SearchOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "mcp__app__thread_read" {
		t.Fatalf("exact tool not first: %+v", got)
	}
}

func TestSearchToolsExactNameDoesNotExpandScope(t *testing.T) {
	catalog := []ToolSummary{
		{Name: "mcp__app__queue_read", Namespace: "allowed", Description: "Read a queued message of this thread."},
		{Name: "mcp__app__thread_read", Namespace: "other", Description: "Read durable state and a paginated timeline from any thread. The default view returns user messages, assistant messages and plans. Continue with afterPosition until hasMore is false."},
	}
	for _, tc := range []struct {
		name, query, ns string
		catalog         []ToolSummary
		want            string
	}{
		{"namespace", "mcp__app__thread_read", "allowed", catalog, "mcp__app__queue_read"},
		{"not-authorized-catalog", "mcp__app__thread_read", "", catalog[:1], "mcp__app__queue_read"},
		{"no-suffix-alias", "thread_read", "", catalog, "mcp__app__queue_read"},
		{"no-case-folded-exact", "MCP__APP__THREAD_READ", "", catalog, "mcp__app__queue_read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SearchTools(tc.catalog, tc.query, SearchOptions{Namespace: tc.ns, Limit: 1})
			if err != nil || len(got) != 1 || got[0].Name != tc.want {
				t.Fatalf("got%+v err%v", got, err)
			}
		})
	}
}

func TestSearchToolsExactNameKeepsBM25Scores(t *testing.T) {
	catalog := []ToolSummary{{Name: "queue_read", Description: "thread read"}, {Name: "thread_read", Description: "thread read detailed long timeline messages plans state"}, {Name: "archive_read", Description: "thread read archive"}}
	ranked, err := SearchTools(catalog, "thread_read", SearchOptions{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := SearchTools(catalog, "thread read", SearchOptions{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(ranked) != 3 || ranked[0].Name != "thread_read" {
		t.Fatalf("%+v", ranked)
	}
	scores := map[string]float64{}
	var remainder []string
	for _, m := range ordinary {
		scores[m.Name] = m.Score
		if m.Name != "thread_read" {
			remainder = append(remainder, m.Name)
		}
	}
	for i, m := range ranked {
		if m.Score != scores[m.Name] {
			t.Fatal("BM25 score changed")
		}
		if i > 0 && m.Name != remainder[i-1] {
			t.Fatal("remaining order changed")
		}
	}
}
