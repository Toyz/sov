// Package static mounts a static-file tree (a built SPA, a docs site, a
// bundle of assets) on the gateway via a RouteHandler. It lets a sov
// binary serve its own frontend — no sidecar nginx/Caddy — so a
// single-binary deploy can host both the API (/rpc/...) and the web app
// (/) from one process.
//
//	//go:embed all:dist
//	var dist embed.FS
//	sub, _ := fs.Sub(dist, "dist")
//	gw.Use(static.New(static.Config{FS: sub, SPAFallback: true}))
//
// The plugin claims its PathPrefix subtree (default "/"). Framework
// endpoints and RPC routes are matched BEFORE plugin routes and win by
// longest-prefix, so mounting at "/" never shadows /rpc/... — see
// gateway/plugin_interfaces.go (RouteHandler match order). The
// static_test.go "rpc not shadowed" case pins this.
package static

import (
	"context"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/Toyz/sov/gateway"
)

// Config configures the static plugin. Provide exactly one source: FS
// (e.g. an embed.FS sub-tree, for single-binary deploys) or Dir (a
// filesystem path, for bind-mounted dev builds). FS wins if both set.
type Config struct {
	// FS is the file tree to serve. Use fs.Sub to strip the embed prefix
	// so paths resolve from the tree root (index.html at FS root).
	FS fs.FS
	// Dir is a filesystem directory to serve when FS is nil.
	Dir string
	// PathPrefix is the mount point. Default "/". A leading slash is
	// added if missing; the subtree (prefix) match is derived from it.
	PathPrefix string
	// SPAFallback serves IndexFile (HTTP 200) for any path that does not
	// resolve to a file — required for client-side routing / deep links
	// in a single-page app. Off → unresolved paths 404.
	SPAFallback bool
	// IndexFile is served for the prefix root and for directory paths,
	// and is the SPAFallback target. Default "index.html".
	IndexFile string
}

// Plugin is the static-file route owner returned by New.
type Plugin struct {
	fsys     fs.FS
	prefix   string
	index    string
	fallback bool
}

// Compile-time proof of the hooks this plugin binds — a signature drift
// here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin       = (*Plugin)(nil)
	_ gateway.PluginDoc    = (*Plugin)(nil)
	_ gateway.RouteHandler = (*Plugin)(nil)
)

// New returns a static plugin from cfg. It panics if neither FS nor Dir
// is set — a misconfigured asset server should fail at boot, not serve
// 404s silently.
func New(cfg Config) *Plugin {
	fsys := cfg.FS
	if fsys == nil {
		if cfg.Dir == "" {
			panic("static.New: one of Config.FS or Config.Dir is required")
		}
		fsys = os.DirFS(cfg.Dir)
	}

	prefix := cfg.PathPrefix
	if prefix == "" {
		prefix = "/"
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}

	index := cfg.IndexFile
	if index == "" {
		index = "index.html"
	}

	return &Plugin{
		fsys:     fsys,
		prefix:   prefix,
		index:    index,
		fallback: cfg.SPAFallback,
	}
}

// PluginName surfaces in /rpc/_introspect.plugins[].
func (p *Plugin) PluginName() string { return "static" }

// Doc surfaces a one-line description in /rpc/_introspect + the explorer.
func (p *Plugin) Doc() string {
	return "Serves a static file tree (e.g. a built SPA) at " + p.prefix + "."
}

// RoutePatterns claims the prefix subtree. A trailing slash → subtree
// (prefix) match per net/http ServeMux convention; the bare prefix is
// claimed too so "/app" (no slash) resolves as well as "/app/".
func (p *Plugin) RoutePatterns() []string {
	if p.prefix == "/" {
		return []string{"/"}
	}
	return []string{p.prefix, p.prefix + "/"}
}

// ServeRoute resolves req.Path to a file under the configured tree.
// Only GET/HEAD are served; everything else is 405. Unresolved paths
// fall back to the index (200) when SPAFallback is set, else 404.
func (p *Plugin) ServeRoute(ctx context.Context, req *gateway.Request) *gateway.Response {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return &gateway.Response{
			Status: http.StatusMethodNotAllowed,
			Header: gateway.Header{"Allow": "GET, HEAD"},
		}
	}

	rel := p.relPath(req.Path)
	if rel == "" {
		return p.serveIndex()
	}

	// path.Clean + the leading-slash strip below neutralize "..":
	// rel never escapes the tree root. fs.ValidPath is the belt to the
	// Clean suspenders — reject anything still non-canonical.
	if !fs.ValidPath(rel) {
		return p.notFound()
	}

	data, ok := p.readFile(rel)
	if !ok {
		// Directory or miss → SPA fallback to index (200) when enabled,
		// else 404.
		return p.notFound()
	}
	return fileResponse(rel, data)
}

// relPath strips the mount prefix and leading slash, returning a
// tree-relative, cleaned path ("" for the prefix root).
func (p *Plugin) relPath(reqPath string) string {
	rest := strings.TrimPrefix(reqPath, p.prefix)
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		return ""
	}
	rest = path.Clean(rest)
	if rest == "." || rest == ".." || strings.HasPrefix(rest, "../") {
		return ""
	}
	return rest
}

// readFile returns the file bytes for a tree-relative path. ok is false
// for a miss OR a directory (callers route both to the index).
func (p *Plugin) readFile(rel string) (data []byte, ok bool) {
	f, err := p.fsys.Open(rel)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || info.IsDir() {
		return nil, false
	}
	b, err := fs.ReadFile(p.fsys, rel)
	if err != nil {
		return nil, false
	}
	return b, true
}

// serveIndex serves the index file. With SPAFallback off and no index
// present, it 404s.
func (p *Plugin) serveIndex() *gateway.Response {
	b, err := fs.ReadFile(p.fsys, p.index)
	if err != nil {
		return p.notFound()
	}
	return fileResponse(p.index, b)
}

func (p *Plugin) notFound() *gateway.Response {
	if p.fallback {
		if b, err := fs.ReadFile(p.fsys, p.index); err == nil {
			return fileResponse(p.index, b)
		}
	}
	return &gateway.Response{
		Status: http.StatusNotFound,
		Header: gateway.Header{"Content-Type": "text/plain; charset=utf-8"},
		Body:   []byte("404 not found"),
	}
}

// fileResponse wraps bytes with a Content-Type derived from the path's
// extension (sniffed when the extension is unknown).
func fileResponse(name string, data []byte) *gateway.Response {
	ct := mime.TypeByExtension(path.Ext(name))
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	return &gateway.Response{
		Status: http.StatusOK,
		Header: gateway.Header{"Content-Type": ct},
		Body:   data,
	}
}
