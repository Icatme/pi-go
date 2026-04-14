package cli

import (
	"context"
	"io"
	"sort"
)

type oauthAuthInfo struct {
	URL          string
	Instructions string
}

type oauthPrompt struct {
	Message string
}

type oauthLoginCallbacks struct {
	OnAuth   func(oauthAuthInfo)
	OnPrompt func(oauthPrompt) (string, error)
	OnOutput func(string)
}

type oauthProvider interface {
	ID() string
	Name() string
	Login(context.Context, oauthLoginCallbacks) (storedOAuthCredentials, error)
}

var builtInOAuthProviders = map[string]oauthProvider{
	"openai-codex": newOpenAICodexOAuthProvider(),
}

func getOAuthProvider(id string) oauthProvider {
	return builtInOAuthProviders[id]
}

func getOAuthProviders() []oauthProvider {
	providers := make([]oauthProvider, 0, len(builtInOAuthProviders))
	for _, provider := range builtInOAuthProviders {
		providers = append(providers, provider)
	}
	sort.Slice(providers, func(i, j int) bool {
		return providers[i].ID() < providers[j].ID()
	})
	return providers
}

func writeLine(out io.Writer, line string) {
	if out == nil {
		return
	}
	_, _ = io.WriteString(out, line)
}
