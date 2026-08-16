package gateway_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	. "github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/internal/gwtest"
	"github.com/Toyz/sov/rpc"
)

type TickerRouter struct{}

type TickParams struct {
	N int `sov:"n"`
}

// Count server-streams integers 0..N-1 as NDJSON.
func (r *TickerRouter) Count(_ *rpc.Context, p *TickParams) (rpc.Stream[int], error) {
	return rpc.StreamOf(func(yield func(int) bool) {
		for i := 0; i < p.N; i++ {
			if !yield(i) {
				return
			}
		}
	}), nil
}

// Boom validates and fails BEFORE returning a stream — must surface as a normal
// buffered error, not a stream.
func (r *TickerRouter) Boom(_ *rpc.Context, _ *TickParams) (rpc.Stream[int], error) {
	return rpc.Stream[int]{}, rpc.BadRequest("nope")
}

func drain(t *testing.T, resp *Response) string {
	t.Helper()
	if resp.Stream == nil {
		t.Fatalf("expected a Stream response, got status=%d body=%s", resp.Status, resp.Body)
	}
	data, err := io.ReadAll(resp.Stream)
	if c, ok := resp.Stream.(io.Closer); ok {
		c.Close()
	}
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	return string(data)
}

func TestStreaming_LocalNDJSON(t *testing.T) {
	gw := gwtest.New()
	gw.Register(&TickerRouter{})

	resp := gw.Dispatch(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Ticker/count", Header: Header{}, Body: []byte(`{"args":[{"n":3}]}`),
	})
	if resp.Status != 200 {
		t.Fatalf("status=%d body=%s", resp.Status, resp.Body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("content-type=%q, want application/x-ndjson", ct)
	}
	if got := drain(t, resp); got != "0\n1\n2\n" {
		t.Fatalf("ndjson = %q, want 0\\n1\\n2\\n", got)
	}
}

// A handler error before the stream is returned must come back as a buffered
// error response (status settable), never a half-open stream.
func TestStreaming_PreStreamErrorIsBuffered(t *testing.T) {
	gw := gwtest.New()
	gw.Register(&TickerRouter{})

	resp := gw.Dispatch(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Ticker/boom", Header: Header{}, Body: []byte(`{"args":[{"n":1}]}`),
	})
	if resp.Stream != nil {
		t.Fatal("a pre-stream error must be buffered, not streamed")
	}
	if resp.Status != 400 {
		t.Fatalf("status=%d body=%s, want 400", resp.Status, resp.Body)
	}
}

// A remote replica emitting NDJSON streams THROUGH the gateway unbuffered —
// streaming survives a mesh hop (W2.7).
func TestStreaming_MeshStreamsThrough(t *testing.T) {
	back := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(200)
		fmt.Fprint(w, "0\n1\n2\n")
	}))
	defer back.Close()

	gw := gwtest.New()
	gw.RegisterResolver().Put("Ticker", back.URL, time.Minute)

	resp := gw.Dispatch(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Ticker/count", Header: Header{}, Body: []byte(`{"args":[{"n":3}]}`),
	})
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
	if got := drain(t, resp); got != "0\n1\n2\n" {
		t.Fatalf("mesh stream-through = %q, want 0\\n1\\n2\\n", got)
	}
}
