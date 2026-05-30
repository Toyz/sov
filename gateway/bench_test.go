package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/batch"
)

// BenchmarkHandleLocal: full gateway dispatch (middleware chain + engine)
// for an in-process service. The PEMM "local" mode — no network.
func BenchmarkHandleLocal(b *testing.B) {
	gw := New()
	gw.Register(&EchoRouter{})
	req := &Request{Method: http.MethodPost, Path: "/rpc/Echo/ping", Header: Header{}, Body: []byte(`{"args":{}}`)}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp := gw.Handle(context.Background(), req)
		if resp.Status != 200 {
			b.Fatalf("status %d", resp.Status)
		}
	}
}

// BenchmarkHandleRemote: the SAME call resolved to a remote pod — one HTTP
// round trip (loopback httptest). The PEMM "remote" mode. The delta vs
// BenchmarkHandleLocal is the cost of the network hop, nothing else: same
// handler contract, same wire shape.
func BenchmarkHandleRemote(b *testing.B) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer remote.Close()

	gw := New()
	gw.RegisterRemote("Echo", remote.URL, time.Minute)
	req := &Request{Method: http.MethodPost, Path: "/rpc/Echo/ping", Header: Header{}, Body: []byte(`{"args":{}}`)}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp := gw.Handle(context.Background(), req)
		if resp.Status != 200 {
			b.Fatalf("status %d body %s", resp.Status, resp.Body)
		}
	}
}

// BenchmarkBatchCoalesce: 5 calls to the SAME remote pod in one /rpc/_batch.
// The batch plugin collapses them into ONE nested /rpc/_batch POST — 1 round
// trip, not 5. Compare ns/op to 5× BenchmarkHandleRemote to see the win.
func BenchmarkBatchCoalesce(b *testing.B) {
	var hits int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		if r.URL.Path == "/rpc/_batch" {
			var br BatchRequest
			_ = json.NewDecoder(r.Body).Decode(&br)
			res := map[string]json.RawMessage{}
			for alias := range br.Calls {
				res[alias] = json.RawMessage(`{"data":{"ok":true}}`)
			}
			body, _ := json.Marshal(BatchResponse{Results: res})
			_, _ = w.Write(body)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer remote.Close()

	gw := New()
	if err := gw.Use(batch.New(batch.Config{})); err != nil {
		b.Fatalf("use batch: %v", err)
	}
	gw.RegisterRemote("Echo", remote.URL, time.Minute)

	calls := map[string]BatchCall{}
	for _, a := range []string{"a", "b", "c", "d", "e"} {
		calls[a] = BatchCall{Service: "Echo", Method: "ping"}
	}
	body, _ := json.Marshal(BatchRequest{Calls: calls})
	req := &Request{Method: http.MethodPost, Path: "/rpc/_batch", Header: Header{}, Body: body}

	// Warm up + assert the collapse really happens (1 HTTP hit for 5 calls).
	atomic.StoreInt64(&hits, 0)
	if resp := gw.Handle(context.Background(), req); resp.Status != 200 {
		b.Fatalf("batch status %d", resp.Status)
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		b.Fatalf("expected 5 calls to coalesce into 1 remote round trip, got %d", got)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp := gw.Handle(context.Background(), req)
		if resp.Status != 200 {
			b.Fatalf("status %d", resp.Status)
		}
	}
}
