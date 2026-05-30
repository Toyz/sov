package static_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/static"
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

func TestNewPanicsWithoutSource(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when neither FS nor Dir set")
		}
	}()
	static.New(static.Config{})
}
