package pigo

import (
	"context"
	"net/http"
)

type CompleteOptions struct {
	APIKey         string
	Auth           map[Provider]AuthConfig
	HTTPClient     *http.Client
	Headers        map[string]string
	MaxTokens      int
	Temperature    *float64
	RequestContext context.Context
}
