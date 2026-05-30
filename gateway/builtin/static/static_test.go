package static_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/static"
	"github.com/Toyz/sov/rpc"
)

func tree() fstest.MapFS {
	return fstest.MapFS{
		"index.html":     {Data: []byte("<!DOCTYPE html><title>app</title>")},
		"assets/app.js":  {Data: []byte("console.log(1)")},
		"assets/app.css": {Data: []byte("body{color:#111}")},
	}
}

func serve(t *testing.T, p *static.Plugin, method, path string) *gateway.Response {
	t.Helper()
	gw := gateway.New()
	gw.ExposeIntrospect() // opt-in endpoint; TestRPCNotShadowed asserts it isn't shadowed
	if err := gw.Use(p); err != nil {
		t.Fatalf("Use static: %v", err)
	}
	return gw.Handle(context.Background(), &gateway.Request{
		Method: method, Path: path, Header: gateway.Header{},
	})
}

func TestServesFileWithContentType(t *testing.T) {
	p := static.New(static.Config{FS: tree()})
	resp := serve(t, p, http.MethodGet, "/assets/app.js")
	if resp.Status != 200 {
		t.Fatalf("status=%d body=%s", resp.Status, resp.Body)
	}
	if string(resp.Body) != "console.log(1)" {
		t.Fatalf("body=%q", resp.Body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("content-type=%q want javascript", ct)
	}
}

func TestRootServesIndex(t *testing.T) {
	p := static.New(static.Config{FS: tree()})
	resp := serve(t, p, http.MethodGet, "/")
	if resp.Status != 200 || !strings.Contains(string(resp.Body), "<!DOCTYPE html>") {
		t.Fatalf("root: status=%d body=%s", resp.Status, resp.Body)
	}
}

func TestSPAFallbackServesIndex(t *testing.T) {
	p := static.New(static.Config{FS: tree(), SPAFallback: true})
	resp := serve(t, p, http.MethodGet, "/pages/deep/link")
	if resp.Status != 200 || !strings.Contains(string(resp.Body), "<!DOCTYPE html>") {
		t.Fatalf("spa fallback: status=%d body=%s", resp.Status, resp.Body)
	}
}

func TestNoFallback404(t *testing.T) {
	p := static.New(static.Config{FS: tree(), SPAFallback: false})
	resp := serve(t, p, http.MethodGet, "/nope/missing")
	if resp.Status != 404 {
		t.Fatalf("want 404, got %d body=%s", resp.Status, resp.Body)
	}
}

func TestTraversalRejected(t *testing.T) {
	// A "../" escape must never read outside the tree. relPath collapses
	// it to the root, so this resolves to the index (no escape), not a
	// parent-dir file.
	p := static.New(static.Config{FS: tree()})
	resp := serve(t, p, http.MethodGet, "/../../etc/passwd")
	if resp.Status != 200 || !strings.Contains(string(resp.Body), "<!DOCTYPE html>") {
		t.Fatalf("traversal: status=%d body=%s", resp.Status, resp.Body)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	p := static.New(static.Config{FS: tree()})
	resp := serve(t, p, http.MethodPost, "/assets/app.js")
	if resp.Status != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", resp.Status)
	}
}

// TestRPCNotShadowed pins the invariant that a "/" mount does not capture
// framework endpoints: /rpc/_introspect must still route to the gateway,
// not the static index.
func TestRPCNotShadowed(t *testing.T) {
	p := static.New(static.Config{FS: tree(), SPAFallback: true})
	resp := serve(t, p, http.MethodGet, "/rpc/_introspect")
	if resp.Status != 200 {
		t.Fatalf("introspect status=%d body=%s", resp.Status, resp.Body)
	}
	if strings.Contains(string(resp.Body), "<!DOCTYPE html>") {
		t.Fatalf("/rpc/_introspect was shadowed by static index: %s", resp.Body)
	}
	// Transport-neutral introspect payload is JSON (the HTTP Server
	// adapter stamps the content-type); confirm it's the catalog, not HTML.
	if !strings.Contains(string(resp.Body), "services") {
		t.Fatalf("introspect body not the catalog: %s", resp.Body)
	}
}

func TestHeadIsHeaderOnly(t *testing.T) {
	p := static.New(static.Config{FS: tree()})
	resp := serve(t, p, http.MethodHead, "/assets/app.js")
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
	if len(resp.Body) != 0 {
		t.Errorf("HEAD returned a body (%d bytes); must be header-only", len(resp.Body))
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "javascript") {
		t.Errorf("HEAD missing Content-Type: %q", resp.Header.Get("Content-Type"))
	}
	if cl := resp.Header.Get("Content-Length"); cl != "14" { // len("console.log(1)")
		t.Errorf("HEAD Content-Length=%q, want 14 (the would-be body size)", cl)
	}
}

func TestSymlinkEscapeBlocked(t *testing.T) {
	// os.Root (via Config.Dir) must NOT follow a symlink that points
	// outside the served tree — os.DirFS would have. Build a dir with
	// index.html + an "escape" symlink → ../secret, and confirm a request
	// for /escape does not read the outside file.
	root := t.TempDir()
	served := root + "/site"
	if err := os.MkdirAll(served, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(served+"/index.html", []byte("<!DOCTYPE html>ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+"/secret", []byte("TOPSECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../secret", served+"/escape"); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	p := static.New(static.Config{Dir: served, SPAFallback: true})
	resp := serve(t, p, http.MethodGet, "/escape")
	if strings.Contains(string(resp.Body), "TOPSECRET") {
		t.Fatalf("symlink escaped the served tree — leaked outside file: %s", resp.Body)
	}
	// Falls back to index (200) since the symlink target is unreadable
	// through the sandboxed root — no escape.
	if !strings.Contains(string(resp.Body), "<!DOCTYPE html>") {
		t.Fatalf("expected SPA fallback to index, got status=%d body=%s", resp.Status, resp.Body)
	}
}

// PingRouter is a minimal business router for the coexistence test.
type PingRouter struct{}

func (PingRouter) Ping(ctx *rpc.Context) (string, error) { return "pong", nil }

// A catch-all static mount at "/" must NOT shadow business RPC: it declines
// the reserved /rpc/ namespace (returns nil), the gateway falls through to
// business dispatch, and "/" still serves the SPA. Exercises the full chain:
// reserved-decline + gateway nil-fall-through.
func TestStaticDoesNotShadowBusinessRPC(t *testing.T) {
	gw := gateway.New()
	gw.Register(&PingRouter{})
	gw.MustUse(static.New(static.Config{FS: tree(), SPAFallback: true})) // catch-all "/"

	// Business RPC dispatches, not shadowed by the "/" mount.
	r := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost, Path: "/rpc/Ping/ping",
		Header: gateway.Header{}, Body: []byte(`{"args":[]}`),
	})
	if r.Status != 200 || !strings.Contains(string(r.Body), "pong") {
		t.Fatalf("business RPC shadowed by catch-all static: status=%d body=%s", r.Status, r.Body)
	}

	// "/" still serves the SPA index.
	idx := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodGet, Path: "/", Header: gateway.Header{},
	})
	if idx.Status != 200 || !strings.Contains(string(idx.Body), "<!DOCTYPE html>") {
		t.Fatalf("static / not served alongside business RPC: status=%d body=%s", idx.Status, idx.Body)
	}
}

func TestNewPanicsWithoutSource(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when neither FS nor Dir set")
		}
	}()
	static.New(static.Config{})
}
