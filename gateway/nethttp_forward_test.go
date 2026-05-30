package gateway

import (
	"context"
	"net/http/httptest"
	"testing"
)

// The HTTP adapter must forward EVERY path to the gateway, not just /rpc/ +
// /health — otherwise a RouteHandler plugin claiming a non-/rpc prefix (e.g.
// a static SPA at "/") never reaches dispatch. Regression for the bug where
// serve() pre-filtered paths and 404'd "/" at the adapter.
func TestNetHTTP_ForwardsAllPaths(t *testing.T) {
	s := NewNetHTTPServer(NetHTTPOptions{})
	var got string
	s.Handle(func(ctx context.Context, req *Request) *Response {
		got = req.Path
		return &Response{Status: 200, Body: []byte("ok")}
	})

	for _, p := range []string{"/", "/index.html", "/assets/app.js", "/app/deep/link", "/rpc/Foo/bar"} {
		got = ""
		rec := httptest.NewRecorder()
		s.serve(rec, httptest.NewRequest("GET", p, nil))
		if got != p {
			t.Errorf("path %q did not reach dispatch (handler saw %q, adapter status=%d) — adapter must forward all paths", p, got, rec.Code)
		}
	}
}
