package superagent

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"
	"sync"
	"unicode/utf8"
)

// HistoryRef identifies public content in one journal's durable main-session
// domain. It is not a capability, an attachment offer, or a control target.
type HistoryRef struct {
	Domain string `json:"domain"`
	Kind   string `json:"kind"` // trigger, thread, task, report
	ID     string `json:"id"`
}

type HistoryLink struct {
	Relation string     `json:"relation"`
	Ref      HistoryRef `json:"ref"`
	Summary  string     `json:"summary,omitempty"`
}

// HistoryRequest continues one object, not a query language. Limits include
// the full JSON response and apply independently to every request.
type HistoryRequest struct {
	Ref      HistoryRef `json:"ref"`
	Cursor   string     `json:"cursor,omitempty"`
	MaxBytes int        `json:"max_bytes"` // 512–16384
	MaxItems int        `json:"max_items"` // 1–32 links
}

// HistoryPage.Data is a UTF-8 chunk of one JSON document. Concatenate Data
// until Next is empty before decoding it. Links have their own ordered stream.
// Pages are detached from coordinator state; no Worker transcript is exposed.
type HistoryPage struct {
	Ref   HistoryRef    `json:"ref"`
	Data  string        `json:"data,omitempty"`
	Links []HistoryLink `json:"links,omitempty"`
	Next  string        `json:"next,omitempty"`
}

type historyKey struct{ kind, id string }
type historyNode struct {
	data  string
	links []HistoryLink
}
type historyCursor struct {
	View string     `json:"view"`
	Ref  HistoryRef `json:"ref"`
	Text int        `json:"text"`
	Link int        `json:"link"`
}

// MainHistory is a read-only, immutable view owned by one main callback. It
// expires when the callback returns, or the coordinator context is canceled.
// Each view permits at most 64 attempted reads and 256 KiB of response JSON.
// References survive restart; cursors are specific to this invocation/view.
// Records exceeding the invocation budget fail explicitly; this API does not
// provide cross-invocation continuation or promise complete unbounded recall.
// The host must use a separate journal for each authorization domain.
type MainHistory struct {
	mu     sync.Mutex
	ctx    context.Context
	domain string
	view   string
	nodes  map[historyKey]*historyNode
	closed bool
	calls  int
	bytes  int
}

var errHistoryUnavailable = errors.New("superagent: history reference or view is unavailable")

func (h *MainHistory) close() {
	if h != nil {
		h.mu.Lock()
		h.closed = true
		h.nodes = nil
		h.mu.Unlock()
	}
}

// Read checks the reference's domain on every hop, including continuations.
// It has no journal writes, execution hooks, or access to private snapshots.
func (h *MainHistory) Read(ctx context.Context, req HistoryRequest) (HistoryPage, error) {
	if h == nil || ctx == nil {
		return HistoryPage{}, errHistoryUnavailable
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return HistoryPage{}, err
	}
	if h.closed || h.ctx == nil || h.ctx.Err() != nil {
		return HistoryPage{}, errHistoryUnavailable
	}
	h.calls++
	if h.calls > 64 || h.bytes >= 256<<10 {
		return HistoryPage{}, errors.New("superagent: history invocation budget exceeded")
	}
	if req.MaxBytes < 512 || req.MaxBytes > 16<<10 || req.MaxItems < 1 || req.MaxItems > 32 {
		return HistoryPage{}, errors.New("superagent: invalid history page limits")
	}
	// A reference from another journal never falls back to its local ID.
	if req.Ref.Domain != h.domain {
		return HistoryPage{}, errHistoryUnavailable
	}
	node := h.nodes[historyKey{req.Ref.Kind, req.Ref.ID}]
	if node == nil {
		return HistoryPage{}, errHistoryUnavailable
	}
	c := historyCursor{View: h.view, Ref: req.Ref}
	if req.Cursor != "" {
		if len(req.Cursor) > 2048 {
			return HistoryPage{}, errHistoryUnavailable
		}
		raw, err := base64.RawURLEncoding.DecodeString(req.Cursor)
		if err != nil || json.Unmarshal(raw, &c) != nil || c.View != h.view || c.Ref != req.Ref || c.Text < 0 || c.Text > len(node.data) || c.Link < 0 || c.Link > len(node.links) || (c.Text < len(node.data) && !utf8.RuneStart(node.data[c.Text])) {
			return HistoryPage{}, errHistoryUnavailable
		}
		canonical, _ := json.Marshal(c)
		if base64.RawURLEncoding.EncodeToString(canonical) != req.Cursor {
			return HistoryPage{}, errHistoryUnavailable
		}
	}
	startText, startLink := c.Text, c.Link
	page := HistoryPage{Ref: req.Ref}
	budget := min(req.MaxBytes, (256<<10)-h.bytes)
	// Reserve the exact continuation envelope while fitting data. Search on
	// rune boundaries, so escaping and multi-byte characters count correctly.
	fit := func(textEnd, linkEnd int) (HistoryPage, int) {
		p := HistoryPage{Ref: req.Ref, Data: node.data[startText:textEnd], Links: node.links[startLink:linkEnd]}
		if textEnd < len(node.data) || linkEnd < len(node.links) {
			raw, _ := json.Marshal(historyCursor{View: h.view, Ref: req.Ref, Text: textEnd, Link: linkEnd})
			p.Next = base64.RawURLEncoding.EncodeToString(raw)
		}
		raw, _ := json.Marshal(p)
		return p, len(raw)
	}
	if len(node.data)-startText <= budget && len(node.links)-startLink <= req.MaxItems {
		if complete, size := fit(len(node.data), len(node.links)); size <= budget {
			complete.Links = slices.Clone(complete.Links)
			h.bytes += size
			return complete, nil
		}
	}
	end := startText
	// At most MaxBytes source bytes can fit. No full document re-encoding or
	// content hashing is needed while sizing a continuation.
	bound := min(len(node.data), startText+budget)
	for bound > startText && bound < len(node.data) && !utf8.RuneStart(node.data[bound]) {
		bound--
	}
	lo, hi := startText, bound
	if _, size := fit(bound, startLink); size <= budget {
		// Finishing an object removes its cursor, so the final page can fit
		// even when a shorter, continued page would not.
		end, lo = bound, bound+1
	}
	for lo <= hi {
		mid := lo + (hi-lo)/2
		for mid > startText && mid < len(node.data) && !utf8.RuneStart(node.data[mid]) {
			mid--
		}
		_, size := fit(mid, startLink)
		if size <= budget {
			end = mid
			if mid == bound {
				break
			}
			_, width := utf8.DecodeRuneInString(node.data[mid:])
			lo = mid + width
		} else {
			hi = mid - 1
		}
	}
	c.Text = end
	page, _ = fit(c.Text, c.Link)
	if c.Text == len(node.data) {
		if len(node.links)-startLink <= req.MaxItems {
			if candidate, size := fit(c.Text, len(node.links)); size <= budget {
				c.Link, page = len(node.links), candidate
			}
		}
		for c.Link < len(node.links) && c.Link-startLink < req.MaxItems {
			candidate, size := fit(c.Text, c.Link+1)
			if size > budget {
				break
			}
			c.Link++
			page = candidate
		}
	}
	encoded, _ := json.Marshal(page)
	if len(encoded) > budget || (page.Next != "" && c.Text == startText && c.Link == startLink) {
		return HistoryPage{}, errors.New("superagent: history page budget cannot make progress")
	}
	page.Links = slices.Clone(page.Links)
	h.bytes += len(encoded)
	return page, nil
}

// The catalog is an in-memory index rebuilt from existing durable records.
// Nodes are copy-on-write: active readers keep a consistent old view while
// new reports arrive. Large public content is serialized once, not every turn.
type historyCatalog struct {
	nodes    map[historyKey]*historyNode
	edges    map[historyEdge]bool
	threads  map[[2]string]string
	assigned map[int]string
	events   int
	reports  int
}

type historyEdge struct {
	from, to historyKey
	relation string
}

func (a *SuperAgent) newMainHistory() *MainHistory {
	if a.history == nil {
		a.history = &historyCatalog{nodes: map[historyKey]*historyNode{}, edges: map[historyEdge]bool{}, threads: map[[2]string]string{}, assigned: map[int]string{}}
	}
	c := a.history
	writable := map[historyKey]bool{}
	ref := func(kind, id string) HistoryRef { return HistoryRef{Domain: a.main.id, Kind: kind, ID: id} }
	put := func(kind, id string, value any) {
		raw, _ := json.Marshal(value) // Only coordinator-owned JSON-safe public types.
		key := historyKey{kind, id}
		n := &historyNode{data: string(raw)}
		if old := c.nodes[key]; old != nil {
			n.links = old.links
		}
		c.nodes[key] = n
	}
	link := func(from, to HistoryRef, relation, summary string) {
		key := historyKey{from.Kind, from.ID}
		old := c.nodes[key]
		if old == nil {
			return
		}
		entry := HistoryLink{Relation: relation, Ref: to, Summary: boundedText(summary, 128, "…")}
		edge := historyEdge{from: key, to: historyKey{to.Kind, to.ID}, relation: relation}
		if c.edges[edge] {
			return
		}
		c.edges[edge] = true
		if !writable[key] {
			old = &historyNode{data: old.data, links: slices.Clone(old.links)}
			c.nodes[key] = old
			writable[key] = true
		}
		old.links = append(old.links, entry)
	}
	for i := c.events; i < len(a.events); i++ {
		e := a.events[i]
		if e.admission != i {
			continue
		}
		id := deliveryID(i)
		key := [2]string{e.trigger.Source, e.trigger.Thread}
		if c.threads[key] == "" {
			c.threads[key] = id
			put("thread", id, struct {
				Source string `json:"source"`
				Thread string `json:"thread"`
			}{key[0], key[1]})
		}
		thread := ref("thread", c.threads[key])
		put("trigger", id, e.trigger)
		link(ref("trigger", id), thread, "thread", "")
		link(thread, ref("trigger", id), "trigger", e.trigger.Text)
	}
	c.events = len(a.events)
	// Assignment can follow admission. Scan cheap identity fields only; no
	// snapshot serialization, hashing or private transcript copying occurs.
	for i, e := range a.events {
		if e.task == "" || c.assigned[i] != "" {
			continue
		}
		c.assigned[i] = e.task
		if c.nodes[historyKey{"task", e.task}] == nil {
			put("task", e.task, TaskInfo{ID: e.task, Summary: a.tasks[e.task].summary})
		}
		trigger, task := ref("trigger", deliveryID(e.admission)), ref("task", e.task)
		link(trigger, task, "task", a.tasks[e.task].summary)
		link(task, trigger, "trigger", e.trigger.Text)
	}
	for _, report := range a.reports[c.reports:] {
		put("report", report.DeliveryID, report)
		index, _ := strconv.Atoi(report.DeliveryID[len("delivery-"):])
		trigger, r := ref("trigger", deliveryID(a.events[index-1].admission)), ref("report", report.DeliveryID)
		link(trigger, r, "report", report.Text)
		link(r, trigger, "trigger", "")
		if report.TaskID != "" {
			task := ref("task", report.TaskID)
			put("task", report.TaskID, TaskInfo{ID: report.TaskID, Summary: a.tasks[report.TaskID].summary})
			link(task, r, "report", report.Text)
			link(r, task, "task", "")
		}
	}
	c.reports = len(a.reports)
	return &MainHistory{ctx: a.ctx, domain: a.main.id, view: rand.Text(), nodes: maps.Clone(c.nodes)}
}

func (a *SuperAgent) addHistoryRefs(input *MainInput) {
	i := a.accepted[[2]string{input.Trigger.Source, input.Trigger.ID}]
	input.HistoryRefs = []HistoryRef{{Domain: a.main.id, Kind: "trigger", ID: deliveryID(i)}, {Domain: a.main.id, Kind: "thread", ID: a.history.threads[[2]string{input.Trigger.Source, input.Trigger.Thread}]}}
	if input.Report != nil {
		input.HistoryRefs = append(input.HistoryRefs, HistoryRef{Domain: a.main.id, Kind: "report", ID: input.Report.DeliveryID})
	}
}
