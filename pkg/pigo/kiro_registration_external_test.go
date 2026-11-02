package pigo_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/Icatme/pi-go/pkg/pigo"
)

func TestKiroRegistrationExportedSurface(t *testing.T) {
	source := t.Name()
	var kind pigo.KiroCredentialKind = pigo.KiroCredentialAPIKey
	config := pigo.KiroBufferedConfig{
		API: pigo.API(source), Provider: pigo.Provider("external-kiro-fixture"), Region: "us-east-1", ProfileARN: "local-profile", Origin: "LOCAL_TEST", UserAgent: "local-test/1",
		RequestIDs: func(context.Context) (pigo.KiroRequestIDs, error) { panic("registration must not generate ids") },
		Credentials: func(context.Context, pigo.KiroCredentialScope) (pigo.KiroScopedCredential, error) {
			return pigo.KiroScopedCredential{Kind: kind, Token: "ksk_LOCAL_ONLY"}, nil
		},
		RoundTrip: func(*http.Request) (*http.Response, error) { panic("registration must not execute HTTP") },
	}
	if err := pigo.RegisterKiroBufferedModule(source, config); err != nil {
		t.Fatal(err)
	}
	defer pigo.UnregisterAPIModules(source)
}
