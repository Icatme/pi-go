package superagent

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
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
	data    string
	links   []HistoryLink
	version [sha256.Size]byte
}

// MainHistory excludes gate-rejected admissions and noise, including their
// reports and links. Host audit records are not model-visible public history.
// It is a read-only, immutable view owned by one main callback. It
// expires when the callback returns, or coordinator/journal ownership is canceled.
// Each view permits at most 64 attempted reads and 256 KiB of response JSON.
// A returned cursor can continue in a later callback of the same coordinator,
// only while the referenced public data and ordered links are unchanged. Cursors
// are authenticated, not authorization grants. Restart invalidates them; stable
// references can be read again from the beginning. This does not schedule another
// callback or compact model context. Use a separate journal per authorization domain.
type MainHistory struct {
	mu       sync.Mutex
	ctx      context.Context
	lifetime context.Context
	domain   string
	key      [sha256.Size]byte
	nodes    map[historyKey]*historyNode
	closed   bool
	calls    int
	bytes    int
}

var errHistoryUnavailable = errors.New("superagent: history reference or view is unavailable")

func (h *MainHistory) close() {
	if h != nil {
		h.mu.Lock()
		h.closed = true
		h.nodes = nil
		h.key = [sha256.Size]byte{}
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
	if h.closed || h.ctx == nil || h.ctx.Err() != nil || (h.lifetime != nil && h.lifetime.Err() != nil) || h.key == ([sha256.Size]byte{}) {
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
	text, link := 0, 0
	if req.Cursor != "" {
		var ok bool
		text, link, ok = h.decodeCursor(req.Cursor, req.Ref, node)
		if !ok {
			return HistoryPage{}, errHistoryUnavailable
		}
	}
	startText, startLink := text, link
	page := HistoryPage{Ref: req.Ref}
	budget := min(req.MaxBytes, (256<<10)-h.bytes)
	// Reserve the exact continuation envelope while fitting data. Search on
	// rune boundaries, so escaping and multi-byte characters count correctly.
	fit := func(textEnd, linkEnd int) (HistoryPage, int) {
		p := HistoryPage{Ref: req.Ref, Data: node.data[startText:textEnd], Links: node.links[startLink:linkEnd]}
		if textEnd < len(node.data) || linkEnd < len(node.links) {
			p.Next = h.encodeCursor(req.Ref, node, textEnd, linkEnd)
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
	text = end
	page, _ = fit(text, link)
	if text == len(node.data) {
		if len(node.links)-startLink <= req.MaxItems {
			if candidate, size := fit(text, len(node.links)); size <= budget {
				link, page = len(node.links), candidate
			}
		}
		for link < len(node.links) && link-startLink < req.MaxItems {
			candidate, size := fit(text, link+1)
			if size > budget {
				break
			}
			link++
			page = candidate
		}
	}
	encoded, _ := json.Marshal(page)
	if len(encoded) > budget || (page.Next != "" && text == startText && link == startLink) {
		return HistoryPage{}, errors.New("superagent: history page budget cannot make progress")
	}
	page.Links = slices.Clone(page.Links)
	h.bytes += len(encoded)
	return page, nil
}

// The compact fixed-size token preserves useful space in a 512-byte page.
// Its HMAC covers the format version, exact node digest, both offsets and the
// entire requested reference. No secret is persisted or exposed to a model.
const historyCursorPayloadBytes = 1 + sha256.Size + 8 + 8
const historyCursorBytes = historyCursorPayloadBytes + sha256.Size

func (h *MainHistory) cursorMAC(payload []byte, ref HistoryRef) []byte {
	mac := hmac.New(sha256.New, h.key[:])
	mac.Write(payload)
	raw, _ := json.Marshal(ref)
	mac.Write(raw) // The preceding binary payload has a fixed unambiguous length.
	return mac.Sum(nil)
}

func (h *MainHistory) encodeCursor(ref HistoryRef, node *historyNode, text, link int) string {
	var raw [historyCursorBytes]byte
	raw[0] = 1
	copy(raw[1:1+sha256.Size], node.version[:])
	binary.BigEndian.PutUint64(raw[33:41], uint64(text))
	binary.BigEndian.PutUint64(raw[41:49], uint64(link))
	copy(raw[historyCursorPayloadBytes:], h.cursorMAC(raw[:historyCursorPayloadBytes], ref))
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

func (h *MainHistory) decodeCursor(token string, ref HistoryRef, node *historyNode) (int, int, bool) {
	if len(token) != base64.RawURLEncoding.EncodedLen(historyCursorBytes) {
		return 0, 0, false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != historyCursorBytes || raw[0] != 1 ||
		!hmac.Equal(raw[historyCursorPayloadBytes:], h.cursorMAC(raw[:historyCursorPayloadBytes], ref)) ||
		!hmac.Equal(raw[1:1+sha256.Size], node.version[:]) {
		return 0, 0, false
	}
	text, link := binary.BigEndian.Uint64(raw[33:41]), binary.BigEndian.Uint64(raw[41:49])
	// Check before converting to int, including on 32-bit targets.
	if text > uint64(len(node.data)) || link > uint64(len(node.links)) ||
		(text < uint64(len(node.data)) && !utf8.RuneStart(node.data[int(text)])) {
		return 0, 0, false
	}
	return int(text), int(link), true
}

func (n *historyNode) seal() {
	// Hash once per immutable node revision, after all copy-on-write links have
	// been added. Length framing avoids ambiguous data/link concatenation.
	digest := sha256.New()
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(n.data)))
	digest.Write(size[:])
	digest.Write([]byte(n.data))
	links, _ := json.Marshal(n.links)
	digest.Write(links)
	copy(n.version[:], digest.Sum(nil))
}

// The catalog is an in-memory index rebuilt from existing durable records.
// Nodes are copy-on-write: active readers keep a consistent old view while
// new reports arrive. Large public content is serialized once, not every turn.
type historyCatalog struct {
	key      [sha256.Size]byte
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

// Visibility follows the immutable ingress decision, not the delivery's final
// state: a router may legitimately ignore an admitted public request. Legacy
// and privileged Submit records have no admission gate, but still exclude
// noise. Fanout deliveries inherit their original admission's decision.
func (a *SuperAgent) historyVisible(index int) bool {
	if index < 0 || index >= len(a.events) {
		return false
	}
	root := a.events[index].admission
	if root < 0 || root > index || a.events[root].admission != root {
		return false
	}
	e := a.events[root]
	return e.trigger.Kind != "noise" && (e.admissionRecord == nil || e.admissionRecord.Decision == "accepted")
}

func (a *SuperAgent) newMainHistory() *MainHistory {
	if a.history == nil {
		a.history = &historyCatalog{nodes: map[historyKey]*historyNode{}, edges: map[historyEdge]bool{}, threads: map[[2]string]string{}, assigned: map[int]string{}}
		rand.Read(a.history.key[:])
	}
	c := a.history
	writable := map[historyKey]bool{}
	ref := func(kind, id string) HistoryRef { return HistoryRef{Domain: a.main.id, Kind: kind, ID: id} }
	put := func(kind, id string, value any) {
		raw, _ := json.Marshal(value) // Only coordinator-owned JSON-safe public types.
		key := historyKey{kind, id}
		n := &historyNode{data: string(raw)}
		if old := c.nodes[key]; old != nil {
			n.links = slices.Clone(old.links)
		}
		c.nodes[key] = n
		writable[key] = true
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
		if e.admission != i || !a.historyVisible(i) {
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
		if e.task == "" || c.assigned[i] != "" || !a.historyVisible(i) {
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
		index, _ := strconv.Atoi(report.DeliveryID[len("delivery-"):])
		if !a.historyVisible(index - 1) {
			continue
		}
		put("report", report.DeliveryID, report)
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
	for key := range writable {
		c.nodes[key].seal()
	}
	var lifetime context.Context
	if journal, ok := a.store.(LeasedJournal); ok {
		lifetime = journal.Context()
	}
	return &MainHistory{ctx: a.ctx, lifetime: lifetime, domain: a.main.id, key: c.key, nodes: maps.Clone(c.nodes)}
}

func (a *SuperAgent) addHistoryRefs(input *MainInput) {
	input.HistoryRefs = nil
	i, ok := a.accepted[[2]string{input.Trigger.Source, input.Trigger.ID}]
	if !ok || !a.historyVisible(i) {
		return
	}
	input.HistoryRefs = []HistoryRef{{Domain: a.main.id, Kind: "trigger", ID: deliveryID(i)}, {Domain: a.main.id, Kind: "thread", ID: a.history.threads[[2]string{input.Trigger.Source, input.Trigger.Thread}]}}
	if input.Report != nil {
		input.HistoryRefs = append(input.HistoryRefs, HistoryRef{Domain: a.main.id, Kind: "report", ID: input.Report.DeliveryID})
	}
}
