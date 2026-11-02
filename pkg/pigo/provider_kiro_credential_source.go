package pigo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

var errKiroCredentialSource = errors.New("kiro: explicit credential source failed")

// Scope is an explicit caller assertion, NOT verified token/account ownership.
type kiroCredentialScope struct {
	ProfileARN string
	Region     string
}

type kiroScopedCredential struct {
	Scope kiroCredentialScope
	Kind  kiroCredentialKind
	Token string `json:"-"`
}

func (kiroScopedCredential) String() string   { return "[kiro credential]" }
func (kiroScopedCredential) GoString() string { return "[kiro credential]" }

// A source is called once with an already-validated request scope. It must honor
// context and must not log secrets. No automatic discovery/refresh/retry is done.
type kiroCredentialSource func(context.Context, kiroCredentialScope) (kiroScopedCredential, error)

// newKiroOfflineRequestWithCredentialSource constructs but NEVER sends a request.
// Validation/serialization precede source invocation, so the source cannot alter
// caller inputs and thereby change the request it is authorizing. The source's
// returned scope must exactly match. This guards accidental scope mixups only;
// it cannot prove the token belongs to that account/profile/region.
//
// Returned requests contain secret headers and must not be logged. The source
// is trusted/cooperative; cancellation cannot forcibly preempt it. Inputs must
// not mutate concurrently while validation/serialization is running.
func newKiroOfflineRequestWithCredentialSource(ctx context.Context, conversation Context, schemas map[string]json.RawMessage, options kiroHTTPRequestOptions, source kiroCredentialSource) (*http.Request, error) {
	if source == nil {
		return nil, errKiroCredentialSource
	}
	request, err := newKiroOfflineHTTPRequest(ctx, conversation, schemas, options)
	if err != nil {
		return nil, errKiroCredentialSource
	}
	success := false
	defer func() {
		if !success {
			request.Body.Close()
		}
	}()
	scope := kiroCredentialScope{ProfileARN: options.Identity.ProfileARN, Region: options.Region}
	if ctx.Err() != nil {
		return nil, errKiroCredentialSource
	}
	credential, err := source(ctx, scope)
	if err != nil || ctx.Err() != nil || credential.Scope != scope {
		return nil, errKiroCredentialSource
	}
	headers, err := buildKiroExplicitAuthHeaders(credential.Kind, credential.Token)
	if err != nil || ctx.Err() != nil {
		return nil, errKiroCredentialSource
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if ctx.Err() != nil {
		return nil, errKiroCredentialSource
	}
	success = true
	return request, nil
}
