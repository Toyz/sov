// Package cors adds CORS headers to every response and handles the
// browser's OPTIONS preflight short-circuit.
//
// Two hats:
//   - HeaderParser — intercepts OPTIONS requests and returns the
//     preflight response (204 + A-C-A-* headers). dispatch never
//     runs for preflight.
//   - ResponseInterceptor — adds A-C-A-Origin / Credentials /
//     Expose-Headers to every successful response so browsers
//     accept the body.
//
// Origin policy:
//
//   - Origins([]string) — exact match, "*" allows all (no credentials)
//
//   - OriginFunc(func(origin string) bool) — custom predicate
//
//   - default — "*" with no credentials
//
//     gw.Use(cors.New())                              // permissive default
//     gw.Use(cors.New(cors.Origins("https://app.foo.io"), cors.AllowCredentials()))
package cors

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

// Config configures the cors plugin. Zero-value Config yields
// permissive defaults (any origin, no credentials, GET+POST+OPTIONS,
// Content-Type+Authorization headers, X-Sov-Request-Id exposed).
//
// Origins ["*"] allows any origin AND auto-disables credentials
// (the spec requires it). OriginFunc, when set, takes precedence
// over Origins.
type Config struct {
	Origins          []string                 // exact match list; "*" = allow any
	OriginFunc       func(origin string) bool // custom predicate; overrides Origins
	AllowMethods     []string                 // default GET, POST, OPTIONS
	AllowHeaders     []string                 // default Content-Type, Authorization
	ExposeHeaders    []string                 // default X-Sov-Request-Id
	AllowCredentials bool                     // ignored when origins == "*"
	MaxAge           int                      // preflight cache seconds; 0 = no caching
}

// Plugin is the cors handler returned by New.
type Plugin struct {
	originFn         func(origin string) bool
	allowAny         bool
	allowMethods     []string
	allowHeaders     []string
	exposeHeaders    []string
	allowCredentials bool
	maxAge           int
}

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin              = (*Plugin)(nil)
	_ gateway.PluginDoc           = (*Plugin)(nil)
	_ gateway.PluginDependency    = (*Plugin)(nil)
	_ gateway.HeaderParser        = (*Plugin)(nil)
	_ gateway.ResponseInterceptor = (*Plugin)(nil)
)

// New returns the cors plugin from cfg.
func New(cfg Config) *Plugin {
	p := &Plugin{
		allowMethods:     cfg.AllowMethods,
		allowHeaders:     cfg.AllowHeaders,
		exposeHeaders:    cfg.ExposeHeaders,
		allowCredentials: cfg.AllowCredentials,
		maxAge:           cfg.MaxAge,
	}
	if len(p.allowMethods) == 0 {
		p.allowMethods = []string{"GET", "POST", "OPTIONS"}
	}
	if len(p.allowHeaders) == 0 {
		p.allowHeaders = []string{"Content-Type", "Authorization"}
	}
	if len(p.exposeHeaders) == 0 {
		p.exposeHeaders = []string{"X-Sov-Request-Id"}
	}
	switch {
	case cfg.OriginFunc != nil:
		p.originFn = cfg.OriginFunc
	case len(cfg.Origins) > 0:
		set := map[string]struct{}{}
		anyOrigin := false
		for _, o := range cfg.Origins {
			if o == "*" {
				anyOrigin = true
				continue
			}
			set[o] = struct{}{}
		}
		p.allowAny = anyOrigin
		p.originFn = func(o string) bool {
			if anyOrigin {
				return true
			}
			_, ok := set[o]
			return ok
		}
	default:
		p.allowAny = true
		p.originFn = func(_ string) bool { return true }
	}
	return p
}

// PluginName surfaces in /rpc/_introspect.plugins[].
func (p *Plugin) PluginName() string { return "cors" }

// Doc satisfies gateway.PluginDoc.
func (p *Plugin) Doc() string {
	return "Cross-origin resource sharing — short-circuits OPTIONS preflight + stamps Access-Control-Allow-* response headers per Config."
}

// Requires the request-id plugin — cors's default ExposeHeaders
// lists X-Sov-Request-Id so the browser can read it off responses.
// Without request-id, clients see the header but it's always empty.
// Hard fail-fast so misconfiguration is loud, not subtle.
func (p *Plugin) Requires() []string { return []string{"request-id"} }

// After is a soft hint: cors's ResponseInterceptor runs after
// request-id's so the response carries the id when cors adds the
// Expose-Headers list. Today registration order = run order, so
// operators get this for free by installing requestid first (which
// Requires() enforces anyway).
func (p *Plugin) After() []string { return []string{"request-id"} }

// ParseHeaders short-circuits OPTIONS preflight requests with a 204
// + the preflight A-C-A-* headers. Other methods pass through.
func (p *Plugin) ParseHeaders(req *gateway.Request) *rpc.Error {
	if req.Method != http.MethodOptions {
		return nil
	}
	origin := req.Header.Get("Origin")
	if origin == "" || !p.originFn(origin) {
		// Not a real CORS preflight, or origin disallowed — let it 404.
		return nil
	}
	// Stash the resolved origin on req.Header so the
	// ResponseInterceptor (which has no easy way to re-evaluate the
	// originFn) can apply it without re-running policy.
	req.Header["_cors_preflight_origin"] = origin
	// Return a sentinel error that the response interceptor turns
	// into the preflight 204. Use a custom code so callers can
	// distinguish if needed.
	return &rpc.Error{Status: 204, Code: "CORS_PREFLIGHT", Message: ""}
}

// InterceptResponse adds A-C-A-Origin + related headers to every
// response that came from an allowed origin. Also fills the
// preflight body for the 204 short-circuit above.
func (p *Plugin) InterceptResponse(req *gateway.Request, resp *gateway.Response) error {
	origin := req.Header.Get("Origin")
	if origin == "" {
		return nil
	}
	if !p.originFn(origin) {
		return nil
	}
	if resp.Header == nil {
		resp.Header = gateway.Header{}
	}
	allowOrigin := origin
	if p.allowAny && !p.allowCredentials {
		allowOrigin = "*"
	}
	resp.Header["Access-Control-Allow-Origin"] = allowOrigin
	resp.Header["Vary"] = "Origin"
	if p.allowCredentials && !p.allowAny {
		resp.Header["Access-Control-Allow-Credentials"] = "true"
	}
	if len(p.exposeHeaders) > 0 {
		resp.Header["Access-Control-Expose-Headers"] = strings.Join(p.exposeHeaders, ", ")
	}
	// Preflight: enrich with the method + header lists. The status
	// 204 + CORS_PREFLIGHT code was set by ParseHeaders.
	if req.Method == http.MethodOptions && resp.Status == 204 {
		resp.Header["Access-Control-Allow-Methods"] = strings.Join(p.allowMethods, ", ")
		resp.Header["Access-Control-Allow-Headers"] = strings.Join(p.allowHeaders, ", ")
		if p.maxAge > 0 {
			resp.Header["Access-Control-Max-Age"] = strconv.Itoa(p.maxAge)
		}
		// Clear the error envelope body — preflight is meant to be
		// an empty 204.
		resp.Body = nil
	}
	return nil
}
