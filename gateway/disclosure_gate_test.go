package gateway_test

import (
	"context"
	"net/http"
	"testing"

	. "github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/explorer"
	"github.com/Toyz/sov/gateway/builtin/manifest"
	"github.com/Toyz/sov/gateway/internal/gwtest"
)

func getDisclosure(gw *Gateway, path, bearer string) *Response {
	h := Header{}
	if bearer != "" {
		h.Set("Authorization", "Bearer "+bearer)
	}
	return gw.Handle(context.Background(), &Request{Method: http.MethodGet, Path: path, Header: h})
}

// On a gateway WITH auth configured, the disclosure endpoints (explorer,
// manifest) block anonymous callers (401) and serve an authenticated one (200).
func TestDisclosure_GatedWhenAuthed(t *testing.T) {
	cases := []struct {
		name string
		path string
		plug any
	}{
		{"explorer", "/rpc/_explorer/", explorer.New(explorer.Config{})},
		{"manifest", "/rpc/_manifest", manifest.New(manifest.Config{})},
	}
	for _, tc := range cases {
		gw := gwtest.New()
		gw.RegisterAuth(&AuthRouter{})
		gw.MustUse(tc.plug)
		if r := getDisclosure(gw, tc.path, ""); r.Status != http.StatusUnauthorized {
			t.Fatalf("%s anonymous on authed gateway: status=%d, want 401", tc.name, r.Status)
		}
		if r := getDisclosure(gw, tc.path, "good-x"); r.Status != http.StatusOK {
			t.Fatalf("%s authed on authed gateway: status=%d, want 200; body=%s", tc.name, r.Status, r.Body)
		}
	}
}

// A no-auth (dev) gateway serves disclosure endpoints anonymously.
func TestDisclosure_OpenWhenNoAuth(t *testing.T) {
	gw := gwtest.New()
	gw.MustUse(explorer.New(explorer.Config{}))
	gw.MustUse(manifest.New(manifest.Config{}))
	if r := getDisclosure(gw, "/rpc/_explorer/", ""); r.Status != http.StatusOK {
		t.Fatalf("explorer on no-auth gateway: status=%d, want 200", r.Status)
	}
	if r := getDisclosure(gw, "/rpc/_manifest", ""); r.Status != http.StatusOK {
		t.Fatalf("manifest on no-auth gateway: status=%d, want 200", r.Status)
	}
}

// Public:true serves anonymously even on an authed gateway.
func TestDisclosure_PublicOptIn(t *testing.T) {
	gw := gwtest.New()
	gw.RegisterAuth(&AuthRouter{})
	gw.MustUse(explorer.New(explorer.Config{Public: true}))
	gw.MustUse(manifest.New(manifest.Config{Public: true}))
	if r := getDisclosure(gw, "/rpc/_explorer/", ""); r.Status != http.StatusOK {
		t.Fatalf("public explorer anonymous: status=%d, want 200", r.Status)
	}
	if r := getDisclosure(gw, "/rpc/_manifest", ""); r.Status != http.StatusOK {
		t.Fatalf("public manifest anonymous: status=%d, want 200", r.Status)
	}
}
