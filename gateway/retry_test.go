package gateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/internal/gwtest"
)

func rpcReq(path string, hdr Header) *Request {
	if hdr == nil {
		hdr = Header{}
	}
	return &Request{Method: http.MethodPost, Path: path, Header: hdr, Body: []byte(`{"args":[]}`)}
}

// A dead replica (dial refused = provably never executed) fails over to the
// healthy one on retry — no idempotency assertion needed, because the request
// never ran on the dead pod.
func TestRetry_FailsOverFromDeadReplica(t *testing.T) {
	var liveHits int32
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&liveHits, 1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer live.Close()

	gw := gwtest.New(WithRetries(RetryConfig{MaxAttempts: 4, BaseBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond}))
	reg := gw.RegisterResolver()
	reg.Put("Svc", "http://127.0.0.1:1", time.Minute) // dial refused (port 1)
	reg.Put("Svc", live.URL, time.Minute)

	ok := 0
	for i := 0; i < 6; i++ {
		if gw.Dispatch(context.Background(), rpcReq("/rpc/Svc/do", nil)).Status == 200 {
			ok++
		}
	}
	if ok != 6 {
		t.Fatalf("every request should fail over to the healthy replica; got %d/6 (live hits=%d)", ok, atomic.LoadInt32(&liveHits))
	}
}

// A single upstream returning 503 (ambiguous — may have executed): retried only
// when the caller asserts idempotency via Idempotency-Key.
func TestRetry_5xxGatedByIdempotencyKey(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":{"code":"OVERLOADED","message":"busy"}}`))
	}))
	defer srv.Close()

	gw := gwtest.New(WithRetries(RetryConfig{MaxAttempts: 3, BaseBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond}))
	gw.RegisterResolver().Put("Svc", srv.URL, time.Minute)

	// No Idempotency-Key: a 503 must NOT be retried (could have executed).
	atomic.StoreInt32(&hits, 0)
	gw.Dispatch(context.Background(), rpcReq("/rpc/Svc/do", nil))
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("non-idempotent 5xx must not retry: hits=%d, want 1", got)
	}

	// With Idempotency-Key: retried up to MaxAttempts.
	atomic.StoreInt32(&hits, 0)
	gw.Dispatch(context.Background(), rpcReq("/rpc/Svc/do", Header{"Idempotency-Key": "abc-123"}))
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("idempotent 5xx should retry to MaxAttempts: hits=%d, want 3", got)
	}
}

// Retry is OFF by default — a 503 is returned after a single attempt.
func TestRetry_DisabledByDefault(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":{"code":"OVERLOADED"}}`))
	}))
	defer srv.Close()

	gw := gwtest.New() // no WithRetries
	gw.RegisterResolver().Put("Svc", srv.URL, time.Minute)

	// Even with an Idempotency-Key, retry is off until enabled.
	gw.Dispatch(context.Background(), rpcReq("/rpc/Svc/do", Header{"Idempotency-Key": "x"}))
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("retry off by default: hits=%d, want 1", got)
	}
}
