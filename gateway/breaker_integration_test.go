package gateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/internal/gwtest"
)

// An upstream that is UP but returns 503 to every call must trip the breaker,
// just like an unreachable one — otherwise every caller keeps paying a full
// round-trip to a known-bad pod.
func TestBreaker_TripsOnUpstream5xx(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"UNAVAILABLE","message":"down"}}`))
	}))
	defer upstream.Close()

	gw := gwtest.New()
	gw.RegisterRemote("Widgets", upstream.URL, time.Minute)

	call := func() *Response {
		return gw.Handle(context.Background(), &Request{
			Method: http.MethodPost, Path: "/rpc/Widgets/create",
			Header: Header{}, Body: []byte(`{"args":[{}]}`),
		})
	}

	// Default threshold is 5: the first five reach the upstream and pass its 503
	// through; the sixth finds the breaker open and fails fast.
	for i := 1; i <= 5; i++ {
		resp := call()
		if resp.Status != http.StatusServiceUnavailable {
			t.Fatalf("call %d: want upstream 503 passthrough, got %d body=%s", i, resp.Status, resp.Body)
		}
		if strings.Contains(string(resp.Body), "UPSTREAM_CIRCUIT_OPEN") {
			t.Fatalf("call %d tripped early: %s", i, resp.Body)
		}
	}
	resp := call()
	if !strings.Contains(string(resp.Body), "UPSTREAM_CIRCUIT_OPEN") {
		t.Fatalf("call 6 should be circuit-open after 5 consecutive 5xx, got %d body=%s", resp.Status, resp.Body)
	}
}

// A healthy upstream (200s) must never trip the breaker.
func TestBreaker_HealthyUpstreamStaysClosed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer upstream.Close()

	gw := gwtest.New()
	gw.RegisterRemote("Widgets", upstream.URL, time.Minute)

	for i := 0; i < 12; i++ {
		resp := gw.Handle(context.Background(), &Request{
			Method: http.MethodPost, Path: "/rpc/Widgets/create",
			Header: Header{}, Body: []byte(`{"args":[{}]}`),
		})
		if resp.Status != 200 {
			t.Fatalf("call %d: healthy upstream returned %d body=%s", i, resp.Status, resp.Body)
		}
	}
}
