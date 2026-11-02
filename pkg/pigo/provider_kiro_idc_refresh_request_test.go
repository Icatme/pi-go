package pigo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

func kiroIDCRefreshRequestFixture() kiroIDCRefreshRequestOptions {
	return kiroIDCRefreshRequestOptions{Region: "us-east-1", UserAgent: "local-test/1", ClientID: "PRIVATE_CLIENT", ClientSecret: `PRIVATE_SECRET&"<>`, RefreshToken: `PRIVATE_REFRESH?&="`, MaxBytes: 4096}
}

func TestKiroOfflineIDCRefreshRequestWireAndOwnership(t *testing.T) {
	options := kiroIDCRefreshRequestFixture()
	request, err := newKiroOfflineIDCRefreshRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer request.Body.Close()
	if request.Method != "POST" || request.URL.String() != "https://oidc.us-east-1.amazonaws.com/token" || request.GetBody != nil || request.Header.Get("Content-Type") != "application/json" || request.Header.Get("User-Agent") != options.UserAgent || len(request.Header) != 3 {
		t.Fatal("request shape")
	}
	raw, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if request.ContentLength != int64(len(raw)) {
		t.Fatal("wire length")
	}
	var fields map[string]string
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 4 || fields["clientId"] != options.ClientID || fields["clientSecret"] != options.ClientSecret || fields["refreshToken"] != options.RefreshToken || fields["grantType"] != "refresh_token" {
		t.Fatal("exact JSON values")
	}
	for _, value := range []string{request.URL.String(), fmt.Sprint(request.Header), fmt.Sprintf("%v", options), fmt.Sprintf("%+v", options), fmt.Sprintf("%#v", options)} {
		if strings.Contains(value, "PRIVATE_") {
			t.Fatal("secret outside explicit body")
		}
	}
	encoded, err := json.Marshal(options)
	if err != nil || strings.Contains(string(encoded), "PRIVATE_") {
		t.Fatal("options JSON leak")
	}
	options.ClientSecret = "CHANGED"
	if fields["clientSecret"] != `PRIVATE_SECRET&"<>` {
		t.Fatal("input ownership")
	}
}

func TestKiroOfflineIDCRefreshRequestValidationAndBudget(t *testing.T) {
	base := kiroIDCRefreshRequestFixture()
	request, err := newKiroOfflineIDCRefreshRequest(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(request.Body)
	request.Body.Close()
	base.MaxBytes = len(raw)
	exact, err := newKiroOfflineIDCRefreshRequest(context.Background(), base)
	if err != nil {
		t.Fatal("exact budget", err)
	}
	exact.Body.Close()
	for _, change := range []func(*kiroIDCRefreshRequestOptions){
		func(o *kiroIDCRefreshRequestOptions) { o.MaxBytes-- }, func(o *kiroIDCRefreshRequestOptions) { o.MaxBytes = 0 }, func(o *kiroIDCRefreshRequestOptions) { o.MaxBytes = (1 << 20) + 1 },
		func(o *kiroIDCRefreshRequestOptions) { o.Region = "us-east-1.evil.test" }, func(o *kiroIDCRefreshRequestOptions) { o.Region = "cn-north-1" }, func(o *kiroIDCRefreshRequestOptions) { o.Region = "us-gov-west-1" }, func(o *kiroIDCRefreshRequestOptions) { o.Region = "us-iso-east-1" },
		func(o *kiroIDCRefreshRequestOptions) { o.ClientID = "" }, func(o *kiroIDCRefreshRequestOptions) { o.ClientSecret = " " }, func(o *kiroIDCRefreshRequestOptions) { o.RefreshToken = "PRIVATE_\nTOKEN" }, func(o *kiroIDCRefreshRequestOptions) { o.RefreshToken = string([]byte{0xff}) }, func(o *kiroIDCRefreshRequestOptions) { o.ClientSecret = strings.Repeat("s", 16385) }, func(o *kiroIDCRefreshRequestOptions) { o.UserAgent = "local\r\nsecret" },
	} {
		candidate := base
		change(&candidate)
		request, err := newKiroOfflineIDCRefreshRequest(context.Background(), candidate)
		if request != nil || err == nil || err.Error() != "kiro: invalid offline IDC refresh request" {
			t.Fatal("validation or error redaction")
		}
	}
}

func TestKiroOfflineIDCRefreshRequestContext(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, cancelled} {
		request, err := newKiroOfflineIDCRefreshRequest(ctx, kiroIDCRefreshRequestFixture())
		if request != nil || err == nil {
			t.Fatal("invalid context")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	request, err := newKiroOfflineIDCRefreshRequest(ctx, kiroIDCRefreshRequestFixture())
	if err != nil {
		t.Fatal(err)
	}
	defer request.Body.Close()
	cancel()
	if request.Context().Err() != context.Canceled {
		t.Fatal("lost cancellation")
	}
}
