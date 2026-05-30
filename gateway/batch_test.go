package gateway_test

import (
	. "github.com/Toyz/sov/gateway"

	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// batchCallsSpy returns an httptest.Server that:
//   - records every POST it sees (path + body) into recorder
//   - on /rpc/_batch responds with status batchStatus and body batchBody
//   - on direct /rpc/{router}/{method} POSTs returns 200 with a tiny
//     JSON envelope echoing the path.
func batchCallsSpy(t *testing.T, batchStatus int, batchBody string, hits *atomic.Int32, batchHits *atomic.Int32, capturedHeader *atomic.Value) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/rpc/_batch" {
			batchHits.Add(1)
			if capturedHeader != nil {
				capturedHeader.Store(r.Header.Get(HeaderSubject))
			}
			if batchStatus == 0 {
				batchStatus = http.StatusOK
			}
			w.WriteHeader(batchStatus)
			if batchBody != "" {
				_, _ = io.WriteString(w, batchBody)
			}
			return
		}
		// Direct per-entry path: echo the path.
		body, _ := io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"data":{"path":"`+r.URL.Path+`","body":`+string(body)+`}}`)
	}))
}

// ---- Tests ----------------------------------------------------------------

func TestBatch_LocalOnly_StillFansOut(t *testing.T) {
	gw := newRegistryGateway()
	gw.Register(&EchoRouter{})

	body := []byte(`{"calls":{
		"a":{"service":"Echo","method":"say","args":{"msg":"hi"}},
		"b":{"service":"Echo","method":"ping"}
	}}`)
	resp := gw.Handle(context.Background(), &Request{Method: http.MethodPost, Path: "/rpc/_batch", Header: Header{}, Body: body})
	if resp.Status != 200 {
		t.Fatalf("status=%d body=%s", resp.Status, resp.Body)
	}
	var br BatchResponse
	if err := json.Unmarshal(resp.Body, &br); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(br.Results) != 2 {
		t.Fatalf("results=%v", br.Results)
	}
	if !strings.Contains(string(br.Results["a"]), `"hi"`) {
		t.Fatalf("a missing: %s", br.Results["a"])
	}
	if !strings.Contains(string(br.Results["b"]), `"ok":true`) {
		t.Fatalf("b missing: %s", br.Results["b"])
	}
}

func TestBatch_GroupsByRemoteService(t *testing.T) {
	var hits, batchHits atomic.Int32
	upstream := batchCallsSpy(t, http.StatusOK,
		`{"results":{"a":{"data":1},"b":{"data":2},"c":{"data":3},"d":{"data":4}}}`,
		&hits, &batchHits, nil)
	defer upstream.Close()

	gw := newBatchGateway()
	gw.RegisterRemote("Widgets", upstream.URL, time.Minute)

	body := []byte(`{"calls":{
		"a":{"service":"Widgets","method":"get","args":{"id":1}},
		"b":{"service":"Widgets","method":"get","args":{"id":2}},
		"c":{"service":"Widgets","method":"get","args":{"id":3}},
		"d":{"service":"Widgets","method":"get","args":{"id":4}}
	}}`)
	resp := gw.Handle(context.Background(), &Request{Method: http.MethodPost, Path: "/rpc/_batch", Header: Header{}, Body: body})
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
	if batchHits.Load() != 1 {
		t.Fatalf("expected 1 batch POST, got %d (total hits=%d)", batchHits.Load(), hits.Load())
	}
	var br BatchResponse
	_ = json.Unmarshal(resp.Body, &br)
	if len(br.Results) != 4 {
		t.Fatalf("results=%v", br.Results)
	}
}

func TestBatch_MixedLocalRemote(t *testing.T) {
	var hits, batchHits atomic.Int32
	upstream := batchCallsSpy(t, http.StatusOK,
		`{"results":{"r1":{"data":"r1"},"r2":{"data":"r2"},"r3":{"data":"r3"}}}`,
		&hits, &batchHits, nil)
	defer upstream.Close()

	gw := newBatchGateway()
	gw.Register(&EchoRouter{})
	gw.RegisterRemote("Widgets", upstream.URL, time.Minute)

	body := []byte(`{"calls":{
		"local":{"service":"Echo","method":"ping"},
		"r1":{"service":"Widgets","method":"x"},
		"r2":{"service":"Widgets","method":"y"},
		"r3":{"service":"Widgets","method":"z"}
	}}`)
	resp := gw.Handle(context.Background(), &Request{Method: http.MethodPost, Path: "/rpc/_batch", Header: Header{}, Body: body})
	if resp.Status != 200 {
		t.Fatalf("status=%d body=%s", resp.Status, resp.Body)
	}
	if batchHits.Load() != 1 {
		t.Fatalf("expected exactly 1 batch POST to remote, got %d", batchHits.Load())
	}
	if hits.Load() != 1 {
		t.Fatalf("expected exactly 1 upstream hit total, got %d", hits.Load())
	}
}

func TestBatch_SingleRemoteUsesDirect(t *testing.T) {
	var hits, batchHits atomic.Int32
	upstream := batchCallsSpy(t, http.StatusOK, "", &hits, &batchHits, nil)
	defer upstream.Close()

	gw := newBatchGateway()
	gw.RegisterRemote("Widgets", upstream.URL, time.Minute)

	body := []byte(`{"calls":{"only":{"service":"Widgets","method":"x"}}}`)
	resp := gw.Handle(context.Background(), &Request{Method: http.MethodPost, Path: "/rpc/_batch", Header: Header{}, Body: body})
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
	if batchHits.Load() != 0 {
		t.Fatalf("single entry must NOT trigger nested batch; got %d", batchHits.Load())
	}
	if hits.Load() != 1 {
		t.Fatalf("expected exactly 1 direct POST, got %d", hits.Load())
	}
}

func TestBatch_FallbackOn404(t *testing.T) {
	var hits, batchHits atomic.Int32
	upstream := batchCallsSpy(t, http.StatusNotFound, "", &hits, &batchHits, nil)
	defer upstream.Close()

	gw := newBatchGateway()
	gw.RegisterRemote("Widgets", upstream.URL, time.Minute)

	body := []byte(`{"calls":{
		"a":{"service":"Widgets","method":"x"},
		"b":{"service":"Widgets","method":"y"},
		"c":{"service":"Widgets","method":"z"}
	}}`)
	first := gw.Handle(context.Background(), &Request{Method: http.MethodPost, Path: "/rpc/_batch", Header: Header{}, Body: body})
	if first.Status != 200 {
		t.Fatalf("first status=%d", first.Status)
	}
	if batchHits.Load() != 1 {
		t.Fatalf("expected 1 batch attempt before fallback, got %d", batchHits.Load())
	}
	// 1 batch attempt + 3 fallback per-entry POSTs = 4 hits.
	if hits.Load() != 4 {
		t.Fatalf("expected 4 total hits (1 batch + 3 fallback), got %d", hits.Load())
	}

	// Second batch within TTL skips the rebatch attempt entirely.
	hits.Store(0)
	batchHits.Store(0)
	second := gw.Handle(context.Background(), &Request{Method: http.MethodPost, Path: "/rpc/_batch", Header: Header{}, Body: body})
	if second.Status != 200 {
		t.Fatalf("second status=%d", second.Status)
	}
	if batchHits.Load() != 0 {
		t.Fatalf("cache hit must skip rebatch, got %d batch attempts", batchHits.Load())
	}
	if hits.Load() != 3 {
		t.Fatalf("expected 3 fallback hits (cache hit, no rebatch), got %d", hits.Load())
	}
}

func TestBatch_PropagatesAuthHeaders(t *testing.T) {
	var hits, batchHits atomic.Int32
	var captured atomic.Value
	upstream := batchCallsSpy(t, http.StatusOK,
		`{"results":{"a":{"data":1},"b":{"data":2}}}`,
		&hits, &batchHits, &captured)
	defer upstream.Close()

	gw := newBatchGateway()
	gw.RegisterRemote("Widgets", upstream.URL, time.Minute)

	body := []byte(`{"calls":{
		"a":{"service":"Widgets","method":"x"},
		"b":{"service":"Widgets","method":"y"}
	}}`)
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/_batch",
		Header: Header{},
		Body:   body,
		User:   &Claims{Subject: "alice"},
	})
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
	if batchHits.Load() != 1 {
		t.Fatalf("expected 1 batch POST, got %d", batchHits.Load())
	}
	if v, _ := captured.Load().(string); v != "alice" {
		t.Fatalf("X-Sov-Subject on rebatched POST = %q, want %q", v, "alice")
	}
}

func TestBatch_UpstreamFailureSetsAllAliases(t *testing.T) {
	var hits, batchHits atomic.Int32
	upstream := batchCallsSpy(t, http.StatusServiceUnavailable, `{"error":{"message":"down"}}`, &hits, &batchHits, nil)
	defer upstream.Close()

	gw := newBatchGateway()
	gw.RegisterRemote("Widgets", upstream.URL, time.Minute)

	body := []byte(`{"calls":{
		"a":{"service":"Widgets","method":"x"},
		"b":{"service":"Widgets","method":"y"}
	}}`)
	resp := gw.Handle(context.Background(), &Request{Method: http.MethodPost, Path: "/rpc/_batch", Header: Header{}, Body: body})
	if resp.Status != 200 {
		t.Fatalf("envelope status=%d", resp.Status)
	}
	var br BatchResponse
	_ = json.Unmarshal(resp.Body, &br)
	for _, alias := range []string{"a", "b"} {
		got := string(br.Results[alias])
		if !strings.Contains(got, "UPSTREAM_UNAVAILABLE") {
			t.Fatalf("alias %s missing failure envelope: %s", alias, got)
		}
	}
}

func TestBatch_UnknownServiceLandsAsNotFound(t *testing.T) {
	gw := newBatchGateway()
	body := []byte(`{"calls":{"a":{"service":"Ghost","method":"x"}}}`)
	resp := gw.Handle(context.Background(), &Request{Method: http.MethodPost, Path: "/rpc/_batch", Header: Header{}, Body: body})
	if resp.Status != 200 {
		t.Fatalf("envelope status=%d", resp.Status)
	}
	var br BatchResponse
	_ = json.Unmarshal(resp.Body, &br)
	if !strings.Contains(string(br.Results["a"]), "NOT_FOUND") {
		t.Fatalf("unresolved alias should land NOT_FOUND: %s", br.Results["a"])
	}
}
