package pigo

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// Do not send API-key headers or replayable request bodies to another origin.
// ErrUseLastResponse avoids embedding the untrusted Location URL in an error.
// The original response remains subject to the provider's existing error policy.
func checkOwnedProviderRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 || !sameProviderHTTPOrigin(via[0].URL, req.URL) {
		return http.ErrUseLastResponse
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}

func sameProviderHTTPOrigin(a, b *url.URL) bool {
	if a == nil || b == nil || a.Opaque != "" || b.Opaque != "" || a.User != nil || b.User != nil {
		return false
	}
	scheme := strings.ToLower(a.Scheme)
	if (scheme != "http" && scheme != "https") || scheme != strings.ToLower(b.Scheme) || a.Hostname() == "" || !sameProviderRedirectHostname(a.Hostname(), b.Hostname()) {
		return false
	}
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if strings.EqualFold(u.Scheme, "https") {
			return "443"
		}
		return "80"
	}
	return port(a) == port(b)
}

func sameProviderRedirectHostname(a, b string) bool {
	if strings.Contains(a, "%") || strings.Contains(b, "%") {
		return a == b
	}
	return strings.EqualFold(a, b)
}
