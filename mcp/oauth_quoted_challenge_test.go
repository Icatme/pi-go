package mcp

import (
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

type quotedChallengeTransport struct {
	base          http.RoundTripper
	force403      bool
	override      string
	metadataReads *atomic.Int32
}

func (x quotedChallengeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/resource-metadata" && x.metadataReads != nil {
		x.metadataReads.Add(1)
	}
	resp, err := x.base.RoundTrip(r)
	if err == nil && r.URL.Path == "/mcp" {
		resp.Header.Set("WWW-Authenticate", `Basic realm="gateway\\", `+resp.Header.Get("WWW-Authenticate"))
		if x.override != "" {
			resp.Header.Set("WWW-Authenticate", x.override)
		}
		if x.force403 {
			resp.StatusCode = 403
		}
	}
	return resp, err
}
func TestOAuthQuotedChallengeActualFlow(t *testing.T) {
	for _, forbidden := range []bool{false, true} {
		t.Run(map[bool]string{false: "401", true: "403"}[forbidden], func(t *testing.T) {
			f := newOAuthFixture(t)
			f.scope = "write"
			opts := f.options()
			c := *opts.SDKConfig.Client
			var metadataReads atomic.Int32
			c.Transport = quotedChallengeTransport{base: http.DefaultTransport, force403: forbidden, metadataReads: &metadataReads}
			opts.SDKConfig.Client = &c
			o, err := NewOAuth(t.Context(), f.key(), opts)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { o.Close() })
			if err = o.Authenticate(t.Context()); err != nil {
				t.Fatalf("valid escaped challenge rejected: %v", err)
			}
			f.mu.Lock()
			requested := append([]string(nil), f.requests...)
			f.mu.Unlock()
			var scopes []string
			if len(requested) == 1 {
				scopes = strings.Fields(requested[0])
				slices.Sort(scopes)
			}
			if !reflect.DeepEqual(scopes, []string{"offline_access", "write"}) {
				t.Fatalf("Bearer scope lost: %v", requested)
			}
			if metadataReads.Load() != 1 {
				t.Fatalf("custom metadata reads=%d", metadataReads.Load())
			}
			if !o.State().Authenticated || f.exchanges.Load() != 1 {
				t.Fatalf("not authenticated: %+v exchanges %d", o.State(), f.exchanges.Load())
			}
		})
	}
}

func TestOAuthSplitChallengeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name         string
		fields, want []string
		bad          bool
	}{
		{"combined", []string{`Basic realm="gateway", Bearer scope="read"`}, []string{`Basic realm="gateway"`, ` Bearer scope="read"`}, false},
		{"multiplefields", []string{`Basic realm="gateway\\"`, `Bearer scope="read"`}, []string{`Basic realm="gateway\\"`, `Bearer scope="read"`}, false},
		{"comma", []string{`Basic realm="a,b", Bearer scope="read", error="insufficient_scope"`}, []string{`Basic realm="a,b"`, ` Bearer scope="read", error="insufficient_scope"`}, false},
		{"even", []string{`Basic realm="gateway\\", Bearer scope="read"`}, []string{`Basic realm="gateway\\"`, ` Bearer scope="read"`}, false},
		{"four", []string{`Basic realm="gateway\\\\", Bearer scope="read"`}, []string{`Basic realm="gateway\\\\"`, ` Bearer scope="read"`}, false},
		{"odd", []string{`Basic realm="a\",b", Bearer scope="read"`}, []string{`Basic realm="a\",b"`, ` Bearer scope="read"`}, false},
		{"three", []string{`Basic realm="a\\\",b", Bearer scope="read"`}, []string{`Basic realm="a\\\",b"`, ` Bearer scope="read"`}, false},
		{"bws", []string{`Bearer scope = "read", error = "insufficient_scope"`}, []string{`Bearer scope = "read", error = "insufficient_scope"`}, false},
		{"twoBearer", []string{`Bearer scope="first", Bearer scope="second"`}, []string{`Bearer scope="first"`, ` Bearer scope="second"`}, false},
		{"unclosed", []string{`Basic realm="secret`}, nil, true},
		{"escapedclosing", []string{`Basic realm="secret\"`}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := authSplitChallenges(tc.fields)
			if tc.bad {
				if err == nil || strings.Contains(err.Error(), "secret") {
					t.Fatalf("unsafe error %v", err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got%q want%q err%v", got, tc.want, err)
			}
			if tc.name == "bws" {
				// SDK v1.8.0 separately rejects BWS between key and equals; do not rewrite parameters here.
				if _, err := oauthex.ParseWWWAuthenticate(got); err == nil {
					t.Fatal("update known SDK BWS boundary")
				}
				return
			}
			if _, err = oauthex.ParseWWWAuthenticate(got); err != nil {
				t.Fatalf("SDK parse: %v", err)
			}
		})
	}
}

func TestOAuthQuotedChallengeRejectsUnsafeFlow(t *testing.T) {
	for _, tc := range []struct {
		name, header           string
		forbidden, wrongIssuer bool
	}{
		{"unclosed401", `Basic realm="challenge-secret`, false, false},
		{"unclosed403", `Basic realm="challenge-secret`, true, false},
		{"scopeOnlyOnBasic", `Basic realm="gateway\\", error="insufficient_scope", scope="write", Bearer realm="mcp"`, true, false},
		{"issuerMismatch", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOAuthFixture(t)
			if tc.wrongIssuer {
				f.iss = "https://wrong-issuer.example"
			}
			opts := f.options()
			c := *opts.SDKConfig.Client
			c.Transport = quotedChallengeTransport{base: http.DefaultTransport, force403: tc.forbidden, override: tc.header}
			opts.SDKConfig.Client = &c
			o, err := NewOAuth(t.Context(), f.key(), opts)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { o.Close() })
			err = o.Authenticate(t.Context())
			if err == nil || strings.Contains(err.Error(), "challenge-secret") {
				t.Fatalf("unsafe result %v", err)
			}
			if o.State().Authenticated || f.exchanges.Load() != 0 {
				t.Fatalf("published/exchanged credentials: %+v %d", o.State(), f.exchanges.Load())
			}
		})
	}
}

func TestOAuthSplitChallengeToken68PreservesBytes(t *testing.T) {
	got, err := authSplitChallenges([]string{`Negotiate abc==, Bearer scope="read"`})
	if err != nil || !reflect.DeepEqual(got, []string{`Negotiate abc==`, ` Bearer scope="read"`}) {
		t.Fatalf("got=%q err=%v", got, err)
	}
	// This only verifies list boundaries. It does not add SDK token68 support.
}
