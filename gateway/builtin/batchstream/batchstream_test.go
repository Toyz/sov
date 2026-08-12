package batchstream_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/batchstream"
	"github.com/Toyz/sov/rpc"
)

// ---- early-return paths (no gateway wiring needed) ------------------------

func TestBatchStream_RejectsNonPost(t *testing.T) {
	h := batchstream.New()
	resp := h.ServeRoute(context.Background(), &gateway.Request{Method: http.MethodGet})
	if resp.Status != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.Status)
	}
}

func TestBatchStream_RejectsAnonymous(t *testing.T) {
	h := batchstream.New()
	resp := h.ServeRoute(context.Background(), &gateway.Request{
		Method: http.MethodPost,
		// no User set → anonymous
		Body: []byte(`{"calls":{"a":{"service":"Echo","method":"ping"}}}`),
	})
	if resp.Status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for anonymous", resp.Status)
	}
}

func TestBatchStream_RejectsEmptyCalls(t *testing.T) {
	h := batchstream.New()
	resp := h.ServeRoute(context.Background(), &gateway.Request{
		Method: http.MethodPost,
		User:   "u_alice", // authenticated subject
		Body:   []byte(`{"calls":{}}`),
	})
	if resp.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for empty calls", resp.Status)
	}
}

// ---- streaming happy path (full gateway) ----------------------------------

type bsAuthRouter struct{}

func (bsAuthRouter) Verify(ctx *rpc.Context, p *gateway.VerifyParams) (*gateway.Claims, error) {
	if !strings.HasPrefix(p.Token, "good-") {
		return nil, rpc.Unauthorized("bad token")
	}
	return &gateway.Claims{
		Subject:   "u_" + strings.TrimPrefix(p.Token, "good-"),
		Issuer:    "test",
		ExpiresAt: time.Now().Add(time.Hour).UTC(),
	}, nil
}

type bsEchoRouter struct{}

func (bsEchoRouter) Ping(ctx *rpc.Context) (string, error) { return "pong", nil }

func TestBatchStream_StreamsFramePerEntry(t *testing.T) {
	gw := gateway.New()
	gw.RegisterAuth(&bsAuthRouter{})
	gw.Register(&bsEchoRouter{})
	if err := gw.Use(batchstream.New()); err != nil {
		t.Fatalf("Use batchstream: %v", err)
	}

	resp := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost,
		Path:   "/rpc/_batchstream",
		Header: gateway.Header{"Authorization": "Bearer good-alice"},
		Body:   []byte(`{"calls":{"a":{"service":"bsEcho","method":"ping"},"b":{"service":"bsEcho","method":"ping"}}}`),
	})
	if resp.Status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Status, resp.Body)
	}
	if resp.Stream == nil {
		t.Fatal("expected a streaming response, got none")
	}
	if c, ok := resp.Stream.(interface{ Close() error }); ok {
		defer c.Close()
	}

	aliases := map[string]bool{}
	sc := bufio.NewScanner(resp.Stream)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var frame struct {
			Alias  string          `json:"alias"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("frame not JSON: %q (%v)", line, err)
		}
		if !strings.Contains(string(frame.Result), "pong") {
			t.Fatalf("frame %q result = %s, want pong", frame.Alias, frame.Result)
		}
		aliases[frame.Alias] = true
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan stream: %v", err)
	}
	if !aliases["a"] || !aliases["b"] {
		t.Fatalf("missing frames: got %v, want a+b", aliases)
	}
}
