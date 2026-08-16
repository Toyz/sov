// Package profiler serves the stdlib net/http/pprof endpoints at
// /debug/pprof/ as a gateway RouteHandler. Off by default (opt-in via
// gw.Use(profiler.New())) and auth-gated on an authed gateway, so a
// production binary exposes CPU/heap/goroutine profiling only to an
// authenticated operator.
//
//	gw.Use(profiler.New())                          // gated when the gateway has auth
//	gw.Use(profiler.New(profiler.Config{Public: true})) // intentionally open
//
// go tool pprof http://host/debug/pprof/heap
// go tool pprof "http://host/debug/pprof/profile?seconds=10"
package profiler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	nhpprof "net/http/pprof"
	"strings"

	"github.com/Toyz/sov/gateway"
)

const prefix = "/debug/pprof"

// Config configures the profiler. Public serves pprof to ANONYMOUS callers
// even on an authed gateway. Default false: profiling data exposes the
// running binary's internals, so an authed gateway requires a subject.
type Config struct {
	Public bool
}

// Plugin is the pprof route owner returned by New.
type Plugin struct {
	gw     *gateway.Gateway
	public bool
	mux    *http.ServeMux
}

// New returns a profiler plugin from cfg.
func New(cfgs ...Config) *Plugin {
	if len(cfgs) > 1 {
		panic("profiler.New: at most one Config")
	}
	var cfg Config
	if len(cfgs) == 1 {
		cfg = cfgs[0]
	}
	// A private mux with the stdlib pprof handlers (the fixed /debug/pprof
	// prefix pprof.Index hard-codes for its named-profile lookup).
	mux := http.NewServeMux()
	mux.HandleFunc(prefix+"/", nhpprof.Index)
	mux.HandleFunc(prefix+"/cmdline", nhpprof.Cmdline)
	mux.HandleFunc(prefix+"/profile", nhpprof.Profile)
	mux.HandleFunc(prefix+"/symbol", nhpprof.Symbol)
	mux.HandleFunc(prefix+"/trace", nhpprof.Trace)
	return &Plugin{public: cfg.Public, mux: mux}
}

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin        = (*Plugin)(nil)
	_ gateway.PluginDoc     = (*Plugin)(nil)
	_ gateway.ConfigApplier = (*Plugin)(nil)
	_ gateway.RouteHandler  = (*Plugin)(nil)
)

// PluginName surfaces in /rpc/_introspect.plugins[].
func (p *Plugin) PluginName() string { return "profiler" }

// Doc surfaces a one-line description in /rpc/_introspect + the explorer.
func (p *Plugin) Doc() string {
	return "Serves net/http/pprof at /debug/pprof/ (opt-in, auth-gated). CPU/heap/goroutine profiling."
}

// Apply grabs the gateway pointer for the auth gate.
func (p *Plugin) Apply(g *gateway.Gateway) error { p.gw = g; return nil }

// RoutePatterns claims the /debug/pprof subtree.
func (p *Plugin) RoutePatterns() []string { return []string{prefix, prefix + "/"} }

// gateAnon 401s an anonymous caller on an authed gateway unless Public.
func (p *Plugin) gateAnon(req *gateway.Request) *gateway.Response {
	if p.public || p.gw == nil || p.gw.AuthBinding() == nil || req.User != nil {
		return nil
	}
	return &gateway.Response{
		Status: http.StatusUnauthorized,
		Header: gateway.Header{"Content-Type": "application/json"},
		Body:   []byte(`{"error":{"code":"UNAUTHORIZED","message":"authentication required for pprof (set profiler Public:true to allow anonymous access)"}}`),
	}
}

// ServeRoute reconstructs an *http.Request (path + RawQuery so ?seconds=/?debug=
// reach pprof) and runs the matching pprof handler into a recorder.
func (p *Plugin) ServeRoute(ctx context.Context, req *gateway.Request) *gateway.Response {
	if resp := p.gateAnon(req); resp != nil {
		return resp
	}
	target := req.Path
	if req.RawQuery != "" {
		target += "?" + req.RawQuery
	}
	hr, err := http.NewRequestWithContext(ctx, req.Method, target, bytes.NewReader(req.Body))
	if err != nil {
		return &gateway.Response{Status: http.StatusInternalServerError, Body: []byte(`{"error":"bad pprof request"}`)}
	}
	for k, v := range req.Header {
		hr.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	p.mux.ServeHTTP(rec, hr)
	hdr := gateway.Header{}
	for k, v := range rec.Header() {
		hdr[k] = strings.Join(v, ",")
	}
	return &gateway.Response{Status: rec.Code, Header: hdr, Body: rec.Body.Bytes()}
}
