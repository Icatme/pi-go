package superagent

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func continuationCatalog(t *testing.T) *SuperAgent {
	t.Helper()
	return &SuperAgent{ctx: t.Context(), main: mainState{id: "domain-a"}, tasks: map[string]*task{}, events: []item{{admission: 0, trigger: Trigger{ID: "large", Source: "im", Thread: "chat", Text: strings.Repeat("<汉🙂", 8000)}}}}
}

func TestMainHistoryContinuesAcrossCallbacks(t *testing.T) {
	a := continuationCatalog(t)
	first := a.newMainHistory()
	ref := HistoryRef{Domain: a.main.id, Kind: "trigger", ID: "delivery-1"}
	p, err := first.Read(t.Context(), HistoryRequest{Ref: ref, MaxBytes: 512, MaxItems: 1})
	if err != nil || p.Next == "" {
		t.Fatalf("first page: %+v %v", p, err)
	}
	first.close()
	next := a.newMainHistory()
	defer next.close()
	got, err := next.Read(t.Context(), HistoryRequest{Ref: ref, Cursor: p.Next, MaxBytes: 512, MaxItems: 1})
	if err != nil || got.Data == "" {
		t.Fatalf("unchanged public object cannot continue in fresh callback: %+v %v", got, err)
	}
}

func TestMainHistoryRejectsForgedCompleteOffsets(t *testing.T) {
	a := continuationCatalog(t)
	h := a.newMainHistory()
	defer h.close()
	ref := HistoryRef{Domain: a.main.id, Kind: "trigger", ID: "delivery-1"}
	p, err := h.Read(t.Context(), HistoryRequest{Ref: ref, MaxBytes: 1024, MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(p.Next)
	if err != nil {
		t.Fatal(err)
	}
	node := h.nodes[historyKey{ref.Kind, ref.ID}]
	// Keep the authentic envelope and signature but forge both end offsets.
	binary.BigEndian.PutUint64(raw[33:41], uint64(len(node.data)))
	binary.BigEndian.PutUint64(raw[41:49], uint64(len(node.links)))
	p, err = h.Read(t.Context(), HistoryRequest{Ref: ref, Cursor: base64.RawURLEncoding.EncodeToString(raw), MaxBytes: 1024, MaxItems: 1})
	if err == nil {
		t.Fatalf("forged end offsets accepted as completion: %+v", p)
	}
}

func TestMainHistoryChecksDirectJournalLifetime(t *testing.T) {
	a := continuationCatalog(t)
	store := leasedMemory(t)
	a.store = store
	h := a.newMainHistory()
	defer h.close()
	store.cancel() // Model a scheduler that has not yet propagated lease loss to a.ctx.
	if a.ctx.Err() != nil {
		t.Fatal("fixture must keep coordinator context alive")
	}
	ref := HistoryRef{Domain: a.main.id, Kind: "trigger", ID: "delivery-1"}
	if p, err := h.Read(context.Background(), HistoryRequest{Ref: ref, MaxBytes: 1024, MaxItems: 1}); err == nil {
		t.Fatalf("canceled journal lifetime leaked another page: %+v", p)
	}
}

func TestMainHistoryContinuationExceedsEachInvocationBudget(t *testing.T) {
	for _, size := range []int{512, 16384} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			a := continuationCatalog(t)
			a.events[0].trigger.Text = strings.Repeat("<", 60000) + strings.Repeat("汉🙂", 500)
			h := a.newMainHistory()
			ref := HistoryRef{Domain: a.main.id, Kind: "trigger", ID: "delivery-1"}
			want := h.nodes[historyKey{ref.Kind, ref.ID}]
			req := HistoryRequest{Ref: ref, MaxBytes: size, MaxItems: 1}
			var data strings.Builder
			var links []HistoryLink
			total, reads, views, exhausted := 0, 0, 1, false
			for {
				p, err := h.Read(t.Context(), req)
				if err != nil {
					if !strings.Contains(err.Error(), "budget") || !reflect.DeepEqual(p, HistoryPage{}) || req.Cursor == "" {
						t.Fatalf("unexpected read failure: %+v %v", p, err)
					}
					if h.calls > 65 || h.bytes > 256<<10 {
						t.Fatal("per-callback budget breached")
					}
					if size == 512 && h.calls != 65 {
						t.Fatal("call budget not exercised")
					}
					if size == 16384 && h.bytes < 240<<10 {
						t.Fatal("byte budget not exercised")
					}
					exhausted = true
					h.close()
					if _, err := h.Read(t.Context(), req); err == nil {
						t.Fatal("expired reader still active")
					}
					h = a.newMainHistory()
					views++
					if views > len(want.data) {
						t.Fatal("continuation did not progress")
					}
					continue
				}
				encoded, _ := json.Marshal(p)
				if len(encoded) > size || len(p.Links) > req.MaxItems || !utf8.ValidString(p.Data) || p.Ref != ref {
					t.Fatal("invalid page")
				}
				total += len(encoded)
				reads++
				data.WriteString(p.Data)
				links = append(links, p.Links...)
				if p.Next == "" {
					break
				}
				if p.Next == req.Cursor || (p.Data == "" && len(p.Links) == 0) {
					t.Fatal("page made no progress")
				}
				req.Cursor = p.Next
			}
			h.close()
			if !exhausted || views < 2 || total <= 256<<10 || (size == 512 && reads <= 64) || data.String() != want.data || !reflect.DeepEqual(links, want.links) {
				t.Fatalf("lost continuation: views=%d bytes=%d reads=%d", views, total, reads)
			}
			var trigger Trigger
			if err := json.Unmarshal([]byte(data.String()), &trigger); err != nil || trigger != a.events[0].trigger {
				t.Fatalf("invalid reconstructed trigger: %v", err)
			}
		})
	}
}

func TestMainHistoryContinuationAuthenticatesEveryField(t *testing.T) {
	a := continuationCatalog(t)
	h := a.newMainHistory()
	defer h.close()
	ref := HistoryRef{Domain: a.main.id, Kind: "trigger", ID: "delivery-1"}
	p, err := h.Read(t.Context(), HistoryRequest{Ref: ref, MaxBytes: 512, MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(p.Next)
	for _, offset := range []int{0, 1, 32, 33, 40, 41, 48, 49, 80} {
		changed := append([]byte(nil), raw...)
		changed[offset] ^= 1
		if _, err := h.Read(t.Context(), HistoryRequest{Ref: ref, Cursor: base64.RawURLEncoding.EncodeToString(changed), MaxBytes: 512, MaxItems: 1}); err == nil {
			t.Fatalf("tampered byte %d accepted", offset)
		}
	}
	for _, token := range []string{p.Next + "=", p.Next[:len(p.Next)-1], "\n" + p.Next, strings.Repeat("x", 2049), base64.RawURLEncoding.EncodeToString([]byte(`{"view":"known","text":0,"link":0}`))} {
		if _, err := h.Read(t.Context(), HistoryRequest{Ref: ref, Cursor: token, MaxBytes: 512, MaxItems: 1}); err == nil {
			t.Fatal("noncanonical/legacy token accepted")
		}
	}
	node := h.nodes[historyKey{ref.Kind, ref.ID}]
	for _, other := range []HistoryRef{{Domain: ref.Domain, Kind: "task", ID: ref.ID}, {Domain: ref.Domain, Kind: ref.Kind, ID: "delivery-2"}, {Domain: "other-domain", Kind: ref.Kind, ID: ref.ID}} {
		h.nodes[historyKey{other.Kind, other.ID}] = node
		h.domain = other.Domain // Prove the MAC binds domain even with identical key/content.
		if _, err := h.Read(t.Context(), HistoryRequest{Ref: other, Cursor: p.Next, MaxBytes: 512, MaxItems: 1}); err == nil {
			t.Fatalf("substituted reference accepted: %+v", other)
		}
	}
	h.domain = ref.Domain
	if _, err := h.Read(t.Context(), HistoryRequest{Ref: ref, Cursor: p.Next, MaxBytes: 512, MaxItems: 1}); err != nil {
		t.Fatalf("tampering corrupted authentic continuation: %v", err)
	}
}

func TestMainHistoryContinuationRejectsChangedDataAndOrderedLinks(t *testing.T) {
	for _, change := range []struct {
		name  string
		apply func(*historyNode)
	}{
		{"data", func(n *historyNode) { n.data += "changed" }},
		{"link-target", func(n *historyNode) { n.links[0].Ref.Domain = "other-domain" }},
		{"link-summary", func(n *historyNode) { n.links[0].Summary = "different summary" }},
		{"link-relation", func(n *historyNode) { n.links[0].Relation = "other" }},
		{"link-order", func(n *historyNode) { n.links[0], n.links[1] = n.links[1], n.links[0] }},
		{"link-append", func(n *historyNode) { n.links = append(n.links, HistoryLink{Relation: "new"}) }},
	} {
		t.Run(change.name, func(t *testing.T) {
			h, ref := historyTestView(strings.Repeat("public ", 1000), 2)
			defer h.close()
			p, err := h.Read(t.Context(), HistoryRequest{Ref: ref, MaxBytes: 512, MaxItems: 1})
			if err != nil {
				t.Fatal(err)
			}
			key := historyKey{ref.Kind, ref.ID}
			original := h.nodes[key]
			updated := &historyNode{data: original.data, links: slices.Clone(original.links)}
			change.apply(updated)
			updated.seal()
			next := &MainHistory{ctx: t.Context(), domain: h.domain, key: h.key, nodes: map[historyKey]*historyNode{key: updated}}
			defer next.close()
			if p, err := next.Read(t.Context(), HistoryRequest{Ref: ref, Cursor: p.Next, MaxBytes: 512, MaxItems: 1}); err == nil || !reflect.DeepEqual(p, HistoryPage{}) {
				t.Fatalf("mixed snapshot returned: %+v %v", p, err)
			}
			if _, err := h.Read(t.Context(), HistoryRequest{Ref: ref, Cursor: p.Next, MaxBytes: 512, MaxItems: 1}); err != nil {
				t.Fatalf("original frozen view changed: %v", err)
			}
			if _, err := next.Read(t.Context(), HistoryRequest{Ref: ref, MaxBytes: 512, MaxItems: 1}); err != nil {
				t.Fatalf("current reference unavailable from start: %v", err)
			}
		})
	}
}

func TestMainHistoryCatalogContinuationTracksOnlyReferencedRevision(t *testing.T) {
	a := continuationCatalog(t)
	h := a.newMainHistory()
	defer h.close()
	ref := HistoryRef{Domain: a.main.id, Kind: "trigger", ID: "delivery-1"}
	thread := HistoryRef{Domain: a.main.id, Kind: "thread", ID: "delivery-1"}
	request := HistoryRequest{Ref: ref, MaxBytes: 512, MaxItems: 1}
	first, err := h.Read(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	// New admission changes the thread, not the immutable original trigger.
	a.events = append(a.events, item{admission: 1, trigger: Trigger{ID: "later", Source: "im", Thread: "chat", Text: "later"}})
	newer := a.newMainHistory()
	defer newer.close()
	request.Cursor = first.Next
	if _, err := newer.Read(t.Context(), request); err != nil {
		t.Fatalf("unrelated node invalidated cursor: %v", err)
	}
	if h.nodes[historyKey{thread.Kind, thread.ID}].version == newer.nodes[historyKey{thread.Kind, thread.ID}].version {
		t.Fatal("changed thread links did not change revision")
	}
	// Publishing a report changes the original trigger's ordered links.
	a.reports = append(a.reports, Report{DeliveryID: "delivery-1", Text: "public report", Status: "completed"})
	latest := a.newMainHistory()
	defer latest.close()
	if _, err := latest.Read(t.Context(), request); err == nil {
		t.Fatal("changed trigger links accepted old cursor")
	}
	if _, err := h.Read(t.Context(), request); err != nil {
		t.Fatalf("old callback lost its frozen snapshot: %v", err)
	}
}
