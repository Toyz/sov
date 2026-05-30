package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Toyz/sov/gateway"
)

func TestAuth_TranslatesClaimsToHeaders(t *testing.T) {
	var captured map[string]string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = map[string]string{
			"sub":    r.Header.Get("X-Forwarded-User"),
			"scopes": r.Header.Get("X-Forwarded-Scopes"),
		}
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	defer upstream.Close()

	gw := gateway.New()
	gw.RegisterRemote("Echo", upstream.URL, time.Minute)
	if err := gw.Use(New(Config{})); err != nil {
		t.Fatalf("Use: %v", err)
	}

	resp := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost, Path: "/rpc/Echo/x",
		Header: gateway.Header{}, Body: []byte(`{"args":{}}`),
		User: &gateway.Claims{Subject: "u_alice", Scopes: []string{"read", "write"}},
	})
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
	if captured["sub"] != "u_alice" {
		t.Errorf("X-Forwarded-User = %q", captured["sub"])
	}
	if captured["scopes"] != "read,write" {
		t.Errorf("X-Forwarded-Scopes = %q", captured["scopes"])
	}
}

func TestAuth_SkipsAnonymous(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Forwarded-User"); got != "" {
			t.Errorf("anonymous should NOT stamp X-Forwarded-User; got %q", got)
		}
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	defer upstream.Close()

	gw := gateway.New()
	gw.RegisterRemote("Echo", upstream.URL, time.Minute)
	_ = gw.Use(New(Config{}))

	gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost, Path: "/rpc/Echo/x",
		Header: gateway.Header{}, Body: []byte(`{"args":{}}`),
	})
}
