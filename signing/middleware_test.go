package signing

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

type EchoRouter struct{}
type EchoSayParams struct {
	Msg string `json:"msg"`
}

func (r *EchoRouter) Say(ctx *rpc.Context, p *EchoSayParams) (map[string]string, error) {
	return map[string]string{"echoed": p.Msg}, nil
}

type SessionRouter struct{}

func (r *SessionRouter) Init(ctx *rpc.Context) (map[string]string, error) {
	return map[string]string{"sid": "stub"}, nil
}

func newGW(t *testing.T) (*gateway.Gateway, *MemoryStore, time.Time) {
	t.Helper()
	store := NewMemoryStore()
	now := time.Unix(1_000_000, 0)
	v := New(Options{Store: store, Now: func() time.Time { return now }})
	mw := GatewayMiddleware(MiddlewareOptions{
		Validator:   v,
		SkipMethods: []string{"Session/init"},
	})
	gw := gateway.New(gateway.WithMiddleware(mw))
	gw.Register(&EchoRouter{})
	gw.Register(&SessionRouter{})
	return gw, store, now
}

func sign(t *testing.T, priv ed25519.PrivateKey, router, method string, body []byte, ts int64) (string, string) {
	t.Helper()
	sig := ed25519.Sign(priv, CanonicalMessage(router, method, body, ts))
	return hex.EncodeToString(sig), strconv.FormatInt(ts, 10)
}

func doReq(gw *gateway.Gateway, path string, body []byte, hdr gateway.Header) *gateway.Response {
	req := &gateway.Request{Method: "POST", Path: path, Header: hdr, Body: body}
	return gw.Handle(context.Background(), req)
}

func TestMiddleware_SkipsSessionInit(t *testing.T) {
	gw, _, _ := newGW(t)
	resp := doReq(gw, "/rpc/Session/init", []byte(`{"args":[]}`), gateway.Header{})
	if resp.Status != 200 {
		t.Fatalf("status = %d, body = %s", resp.Status, resp.Body)
	}
}

func TestMiddleware_RejectsUnsigned(t *testing.T) {
	gw, _, _ := newGW(t)
	resp := doReq(gw, "/rpc/Echo/say", []byte(`{"args":[{"msg":"hi"}]}`), gateway.Header{})
	if resp.Status != 403 {
		t.Fatalf("status = %d", resp.Status)
	}
	if !strings.Contains(string(resp.Body), "MISSING_HEADERS") {
		t.Fatalf("body = %s", resp.Body)
	}
}

func TestMiddleware_PassesSigned(t *testing.T) {
	gw, store, now := newGW(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	store.Put("sid-1", pub)

	body := []byte(`{"args":[{"msg":"hi"}]}`)
	sigHex, tsStr := sign(t, priv, "Echo", "say", body, now.Unix())
	hdr := gateway.Header{"X-Session": "sid-1", "X-Ts": tsStr, "X-Sig": sigHex}
	resp := doReq(gw, "/rpc/Echo/say", body, hdr)
	if resp.Status != 200 {
		t.Fatalf("status = %d, body = %s", resp.Status, resp.Body)
	}
}

func TestMiddleware_FrameworkBypass(t *testing.T) {
	gw, _, _ := newGW(t)
	// _health and _introspect should not be blocked by signing.
	req := &gateway.Request{Method: "GET", Path: "/rpc/_health", Header: gateway.Header{}}
	resp := gw.Handle(context.Background(), req)
	if resp.Status != 200 {
		t.Fatalf("_health status = %d", resp.Status)
	}
}
