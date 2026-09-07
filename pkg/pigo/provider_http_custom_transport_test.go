package pigo

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestProviderHTTPRegisteredTransportRetainsUnknownAfterHiddenSend(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	fallback := &http.Transport{}
	defer fallback.CloseIdleConnections()
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	transport.RegisterProtocol("http", observationRoundTripper(func(r *http.Request) (*http.Response, error) {
		// Adapters using Client.Post create their own context, so the successful
		// send does not emit httptrace events on the outer request's context.
		response, err := server.Client().Post(r.URL.String(), "application/json", r.Body)
		if err != nil {
			return nil, err
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if err := server.Listener.Close(); err != nil {
			return nil, err
		}
		// The standard fallback emits GetConn and a failed ConnectDone for the
		// same URL. Those events cannot disprove the adapter's earlier send.
		return fallback.RoundTrip(r.Clone(r.Context()))
	}))
	var got HTTPObservation
	var observations int
	client := NewProviderHTTPClient(&http.Client{Transport: transport}, ProviderHTTPPolicy{Observe: func(o HTTPObservation) {
		got = o
		observations++
	}})
	request, err := http.NewRequest(http.MethodPost, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(request)
	if err == nil || calls.Load() != 1 || observations != 1 || got.Delivery != HTTPDeliveryUnknown {
		t.Fatalf("server calls=%d observations=%d observation=%+v error=%v", calls.Load(), observations, got, err)
	}
}
