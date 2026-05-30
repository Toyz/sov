package explorer

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static
var staticFS embed.FS

// serveUI resolves an inbound path (already verified to start with
// /rpc/_explorer) and returns (contentType, body, status). The
// "api.json" path is fed by the catalog bytes the plugin already
// fetched via gw.Handle on /rpc/_introspect. Everything else is
// served from the embedded static FS so a production binary ships
// the UI assets in the same artifact as the gateway.
func serveUI(path, prefix string, catalog []byte) (contentType string, body []byte, status int) {
	rel := strings.TrimPrefix(path, prefix)
	switch rel {
	case "", "/":
		b, err := fs.ReadFile(staticFS, "static/index.html")
		if err != nil {
			return "text/plain; charset=utf-8", []byte(err.Error()), http.StatusInternalServerError
		}
		return "text/html; charset=utf-8", b, http.StatusOK
	case "/api.json", "/api-internal.json":
		// Both return the catalog bytes the plugin fetched; ServeRoute
		// already asked the gateway for the public vs full payload based
		// on which path this was.
		return "application/json", catalog, http.StatusOK
	}
	rel = strings.TrimPrefix(rel, "/static/")
	if rel == "" {
		return "text/plain; charset=utf-8", []byte("not found"), http.StatusNotFound
	}
	b, err := fs.ReadFile(staticFS, "static/"+rel)
	if err != nil {
		return "text/plain; charset=utf-8", []byte("not found"), http.StatusNotFound
	}
	return contentTypeFor(rel), b, http.StatusOK
}

func contentTypeFor(name string) string {
	switch {
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".js"):
		return "application/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".json"):
		return "application/json"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}
