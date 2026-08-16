package explorer

import (
	"embed"
	"encoding/json"
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

// explorerManifest lists the plugin extension assets for the browser to load,
// split by kind. Inline (Body) assets resolve to a framework-served URL under
// {prefix}/ext/; URL assets pass through as-is. CSS is <link>ed and JS is
// dynamic-imported by the client in listed order.
func explorerManifest(prefix string, assets []Asset) []byte {
	type manifest struct {
		CSS []string `json:"css"`
		JS  []string `json:"js"`
	}
	out := manifest{CSS: []string{}, JS: []string{}}
	for _, a := range assets {
		url := a.URL
		if url == "" && len(a.Body) > 0 {
			url = prefix + "/ext/" + safeExtName(a.Name) + "." + a.Kind
		}
		if url == "" {
			continue
		}
		switch a.Kind {
		case "css":
			out.CSS = append(out.CSS, url)
		case "js":
			out.JS = append(out.JS, url)
		}
	}
	b, _ := json.Marshal(out)
	return b
}

// serveExtAsset returns the bytes of an inline extension asset addressed as
// {prefix}/ext/{name}.{kind}. Name is matched against the sanitized asset name,
// so a traversal like "../x" can never resolve.
func serveExtAsset(path, prefix string, assets []Asset) (string, []byte, int) {
	rel := strings.TrimPrefix(path, prefix+"/ext/")
	dot := strings.LastIndexByte(rel, '.')
	if dot <= 0 {
		return "text/plain; charset=utf-8", []byte("not found"), http.StatusNotFound
	}
	name, kind := rel[:dot], rel[dot+1:]
	for _, a := range assets {
		if a.Kind == kind && len(a.Body) > 0 && safeExtName(a.Name) == name {
			return contentTypeFor("x." + kind), a.Body, http.StatusOK
		}
	}
	return "text/plain; charset=utf-8", []byte("not found"), http.StatusNotFound
}

// safeExtName reduces an asset name to a safe path segment (letters, digits, '-',
// '_'), so it can never escape {prefix}/ext/.
func safeExtName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "ext"
	}
	return b.String()
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
