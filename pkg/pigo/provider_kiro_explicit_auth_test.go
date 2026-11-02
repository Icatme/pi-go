package pigo

import (
	"reflect"
	"strings"
	"testing"
)

func TestKiroExplicitAuthHeaderKinds(t *testing.T) {
	// All tokens are invented local fixtures, never sent or loaded from a host.
	for _,tc:=range []struct {kind kiroCredentialKind;token,tokenType string}{
		{kiroCredentialBearer,"LOCAL_ONLY_BEARER_FIXTURE",""},
		{kiroCredentialBearer,"LOCAL_ONLY_BEARER_FIXTURE+/.~==",""},
		{kiroCredentialAPIKey,"ksk_LOCAL_ONLY_FAKE_KEY","API_KEY"},
		{kiroCredentialExternalIDP,"LOCAL_ONLY_EXTERNAL_FIXTURE","EXTERNAL_IDP"},
	} {
		headers,err:=buildKiroExplicitAuthHeaders(tc.kind,tc.token)
		expected:=map[string]string{"Authorization":"Bearer "+tc.token}
		if tc.tokenType!="" {expected["tokentype"]=tc.tokenType}
		if err!=nil || !reflect.DeepEqual(headers,expected) {t.Fatal("explicit authentication header contract changed")}
	}
}

func TestKiroExplicitAuthRejectsAndRedactsErrors(t *testing.T) {
	for _,tc:=range []struct {kind kiroCredentialKind;token string}{
		{"unknown","LOCAL_ONLY_SENTINEL"},
		{kiroCredentialBearer,""},
		{kiroCredentialBearer,"LOCAL_ONLY_SENTINEL\r\nInjected: yes"},
		{kiroCredentialBearer,"LOCAL_ONLY_SENTINEL with-space"},
		{kiroCredentialBearer,"LOCAL_ONLY_SENTINEL,other"},
		{kiroCredentialBearer,"=LOCAL_ONLY_SENTINEL"},
		{kiroCredentialBearer,"LOCAL_ONLY=middle"},
		{kiroCredentialBearer,"="},
		{kiroCredentialBearer,"LOCAL_ONLY_SENTINEL\t"},
		{kiroCredentialBearer,"LOCAL_ONLY_SENTINEL\x00"},
		{kiroCredentialBearer,"LOCAL_ONLY_SENTINEL\x7f"},
		{kiroCredentialBearer,"LOCAL_ONLY_SENTINEL非ASCII"},
		{kiroCredentialBearer,string([]byte{255})},
		{kiroCredentialBearer,strings.Repeat("x",16385)},
		{kiroCredentialAPIKey,"LOCAL_ONLY_SENTINEL"},
		{kiroCredentialAPIKey,"ksk_"},
		{kiroCredentialBearer,"ksk_LOCAL_ONLY_SENTINEL"},
		{kiroCredentialExternalIDP,"ksk_LOCAL_ONLY_SENTINEL"},
	} {
		headers,err:=buildKiroExplicitAuthHeaders(tc.kind,tc.token)
		if err==nil || headers!=nil {t.Fatal("invalid authentication accepted")}
		if err.Error()!="kiro: invalid explicit authentication input" {t.Fatal("error disclosed caller data")}
	}
}

func TestKiroExplicitAuthDoesNotInferAuthorityOrShareHeaders(t *testing.T) {
	// A JWT-like value does not silently change an explicit bearer kind.
	const token="LOCAL_ONLY.header.payload"
	first,err:=buildKiroExplicitAuthHeaders(kiroCredentialBearer,token)
	if err!=nil || len(first)!=1 {t.Fatal("bearer kind inferred from token")}
	first["tokentype"]="EXTERNAL_IDP";first["Authorization"]="mutated"
	second,err:=buildKiroExplicitAuthHeaders(kiroCredentialBearer,token)
	if err!=nil || len(second)!=1 || second["Authorization"]!="Bearer "+token {t.Fatal("authentication maps share state")}
	boundary:=strings.Repeat("x",16384)
	if h,err:=buildKiroExplicitAuthHeaders(kiroCredentialBearer,boundary);err!=nil || h["Authorization"]!="Bearer "+boundary {t.Fatal("token boundary changed")}
}
