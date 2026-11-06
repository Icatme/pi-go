package channel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Icatme/pi-go/superagent"
)

type Protocol string

const (
	IM     Protocol = "im"
	GitHub Protocol = "github"
)

// Identity consists exclusively of independently authenticated/authorized host
// facts, never unchecked payload claims or model inferences. EventID is the
// stable callback identity (GitHub delivery ID, not comment/review ID). EventType
// is the authenticated GitHub event header. TaskID and ToThread are host-approved
// routing associations; Normalize never derives routing from text or links.
// MessageID is the authenticated platform comment/review ID for GitHub (required).
// For IM it may be omitted, otherwise it must equal EventID.
type Identity struct {
	Organization, Principal, Thread, EventID string
	ExplicitRequest                          bool
	TaskID                                   string
	ToThread                                 bool
	EventType                                string
	MessageID                                string
}

// Normalize decodes an offline IM simulator or bounded GitHub webhook subset.
// Source is a trusted host namespace, not a payload-controlled platform name.
// IM has exactly five string fields: organization_id, actor_id, thread_id,
// message_id, text. GitHub supports issue_comment created/edited on PRs,
// pull_request_review submitted, and pull_request_review_comment created/edited.
// GitHub Thread is the canonical repositoryID/pull/number string; Organization
// and Principal are canonical decimal owner account (User or Organization) and sender IDs.
// Authentication, authorization policy and replay handling remain host/Admit work.
func Normalize(protocol Protocol, source string, identity Identity, body []byte) (superagent.AdmissionEnvelope, error) {
	var zero superagent.AdmissionEnvelope
	for _, value := range []string{source, identity.Organization, identity.Principal, identity.Thread, identity.EventID} {
		if !ingressIdentity(value) {
			return zero, errors.New("invalid host identity")
		}
	}
	if source == "superagent.background" || (identity.TaskID != "" && (!ingressIdentity(identity.TaskID) || identity.ToThread)) {
		return zero, errors.New("invalid source or routing association")
	}
	if protocol != IM && protocol != GitHub {
		return zero, errors.New("unsupported ingress protocol")
	}
	if (protocol == IM && identity.EventType != "") || (protocol == GitHub && !ingressIdentity(identity.EventType)) {
		return zero, errors.New("invalid host event type")
	}
	root, err := ingressJSON(body)
	if err != nil {
		return zero, err
	}
	if identity.MessageID != "" && !ingressIdentity(identity.MessageID) {
		return zero, errors.New("invalid host message identity")
	}
	var text string
	switch protocol {
	case IM:
		if identity.MessageID != "" && identity.MessageID != identity.EventID {
			return zero, errors.New("IM message and event identity mismatch")
		}
		if len(root) != 5 {
			return zero, errors.New("invalid IM simulator fields")
		}
		for key, want := range map[string]string{"organization_id": identity.Organization, "actor_id": identity.Principal, "thread_id": identity.Thread, "message_id": identity.EventID} {
			if got, ok := root[key].(string); !ok || got != want {
				return zero, fmt.Errorf("IM %s does not match host", key)
			}
		}
		var ok bool
		text, ok = root["text"].(string)
		if !ok {
			return zero, errors.New("IM text must be a string")
		}
	case GitHub:
		text, err = ingressGitHubText(root, identity)
		if err != nil {
			return zero, err
		}
	}
	if len(text) > 64<<10 {
		return zero, errors.New("text exceeds 64 KiB")
	}
	messageID := identity.EventID
	if protocol == GitHub {
		messageID = identity.EventType + ":" + identity.MessageID
	}
	kind := "signal"
	if identity.ExplicitRequest {
		kind = "request"
	}
	return superagent.AdmissionEnvelope{
		Trigger:   superagent.Trigger{ID: identity.EventID, Source: source, Thread: identity.Thread, Kind: kind, Text: text},
		MessageID: messageID,
		Principal: identity.Principal, Group: identity.Organization, ExplicitRequest: identity.ExplicitRequest,
		TaskID: identity.TaskID, ToThread: identity.ToThread,
	}, nil
}

func ingressIdentity(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func ingressGitHubText(root map[string]any, id Identity) (string, error) {
	action, _ := root["action"].(string)
	contentKey, numberKey := "comment", "pull_request"
	switch id.EventType {
	case "issue_comment":
		numberKey = "issue"
		if ingressObject(root, "issue")["pull_request"] == nil {
			return "", errors.New("issue comment is not on a pull request")
		}
		if _, ok := ingressObject(root, "issue")["pull_request"].(map[string]any); !ok {
			return "", errors.New("invalid issue pull_request")
		}
		if action != "created" && action != "edited" {
			return "", errors.New("unsupported issue comment action")
		}
	case "pull_request_review_comment":
		if action != "created" && action != "edited" {
			return "", errors.New("unsupported review comment action")
		}
	case "pull_request_review":
		contentKey = "review"
		if action != "submitted" {
			return "", errors.New("unsupported review action")
		}
	default:
		return "", errors.New("unsupported GitHub event type")
	}
	repo, sender, content := ingressObject(root, "repository"), ingressObject(root, "sender"), ingressObject(root, contentKey)
	owner := ingressObject(repo, "owner")
	org, actor, repository, number := ingressDecimalID(owner["id"]), ingressDecimalID(sender["id"]), ingressDecimalID(repo["id"]), ingressDecimalID(ingressObject(root, numberKey)["number"])
	if kind, exists := owner["type"]; exists && kind != "Organization" && kind != "User" {
		return "", errors.New("unsupported GitHub owner type")
	}
	if ingressDecimalID(content["id"]) == "" || ingressDecimalID(content["id"]) != id.MessageID {
		return "", errors.New("GitHub message identity mismatch")
	}
	if org == "" || actor == "" || repository == "" || number == "" ||
		org != id.Organization || actor != id.Principal || repository+"/pull/"+number != id.Thread || ingressDecimalID(ingressObject(content, "user")["id"]) != actor {
		return "", errors.New("GitHub identity does not match host")
	}
	if organization, exists := root["organization"]; exists {
		o, ok := organization.(map[string]any)
		if !ok || ingressDecimalID(o["id"]) != org {
			return "", errors.New("GitHub organization mismatch")
		}
	}
	text, ok := content["body"].(string)
	if !ok {
		return "", errors.New("GitHub body must be a string")
	}
	return text, nil
}

func ingressObject(parent map[string]any, key string) map[string]any {
	value, _ := parent[key].(map[string]any)
	return value
}

func ingressDecimalID(value any) string {
	n, ok := value.(json.Number)
	if !ok {
		return ""
	}
	s := string(n)
	if len(s) == 0 || len(s) > 20 || s[0] < '1' || s[0] > '9' {
		return ""
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return ""
		}
	}
	if _, err := strconv.ParseUint(s, 10, 64); err != nil {
		return ""
	}
	return s
}

// The standard decoder otherwise accepts duplicate keys and replaces invalid
// Unicode surrogate escapes. Reject both, including in irrelevant extra fields.
func ingressJSON(body []byte) (map[string]any, error) {
	if len(body) == 0 || len(body) > 1<<20 || !utf8.Valid(body) {
		return nil, errors.New("invalid JSON size or UTF-8")
	}
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' {
			continue
		}
		i++
		if i >= len(body) {
			break
		}
		if body[i] != 'u' {
			continue
		}
		if i+4 >= len(body) {
			return nil, errors.New("invalid Unicode escape")
		}
		n, err := strconv.ParseUint(string(body[i+1:i+5]), 16, 16)
		if err != nil {
			return nil, errors.New("invalid Unicode escape")
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return nil, errors.New("unpaired Unicode surrogate")
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(body) || body[i+1] != '\\' || body[i+2] != 'u' {
				return nil, errors.New("unpaired Unicode surrogate")
			}
			low, err := strconv.ParseUint(string(body[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return nil, errors.New("unpaired Unicode surrogate")
			}
			i += 6
		}
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	value, err := ingressValue(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON content")
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("JSON root must be an object")
	}
	return root, nil
}

func ingressValue(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New("JSON nesting exceeds 64")
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		m := make(map[string]any)
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			s, ok := key.(string)
			if !ok {
				return nil, errors.New("invalid JSON key")
			}
			if _, exists := m[s]; exists {
				return nil, errors.New("duplicate JSON key")
			}
			v, err := ingressValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			m[s] = v
		}
		_, err := d.Token()
		return m, err
	case json.Delim('['):
		var a []any
		for d.More() {
			v, err := ingressValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		_, err := d.Token()
		return a, err
	default:
		return token, nil
	}
}
