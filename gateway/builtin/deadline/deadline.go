// Package deadline bounds each request to a deadline budget so a mesh chain
// A->B->C shares ONE deadline instead of each hop independently waiting out a
// full timeout (worst case: the sum). As a Middlewarer it derives the request
// ctx's deadline from either an inbound X-Sov-Deadline (stamped by an upstream
// gateway) or a configured default per-call timeout, and cancels the ctx when
// it lapses. The gateway propagates the ctx deadline as X-Sov-Deadline on every
// outbound hop, so the whole chain converges on the earliest deadline.
//
//	gw.Use(deadline.New(deadline.Config{Default: 5 * time.Second}))
package deadline

import (
	"context"
	"strconv"
	"time"

	"github.com/Toyz/sov/gateway"
)

// Header is the wire name carrying an absolute deadline as unix nanoseconds.
const Header = "X-Sov-Deadline"

// Config configures the plugin. Default is the per-call timeout applied when the
// inbound request carries no X-Sov-Deadline; 0 means only an inbound deadline is
// honored (no default cap).
type Config struct {
	Default time.Duration
}

// Plugin is the deadline-budget middleware returned by New.
type Plugin struct {
	def time.Duration
	now func() time.Time
}

// New returns a deadline plugin from cfg.
func New(cfgs ...Config) *Plugin {
	if len(cfgs) > 1 {
		panic("deadline.New: at most one Config")
	}
	var cfg Config
	if len(cfgs) == 1 {
		cfg = cfgs[0]
	}
	return &Plugin{def: cfg.Default, now: time.Now}
}

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin      = (*Plugin)(nil)
	_ gateway.PluginDoc   = (*Plugin)(nil)
	_ gateway.Middlewarer = (*Plugin)(nil)
)

// PluginName surfaces in /rpc/_introspect.plugins[].
func (p *Plugin) PluginName() string { return "deadline" }

// Doc satisfies gateway.PluginDoc.
func (p *Plugin) Doc() string {
	return "Bounds each request to a deadline budget — honors an inbound X-Sov-Deadline or a default timeout and cancels the ctx when it lapses."
}

// Wrap implements Middlewarer.
func (p *Plugin) Wrap(next gateway.Handler) gateway.Handler {
	return func(ctx context.Context, req *gateway.Request) *gateway.Response {
		dl, ok := p.inboundDeadline(req)
		if !ok && p.def > 0 {
			dl, ok = p.now().Add(p.def), true
		}
		if ok {
			// WithDeadline keeps the EARLIER of any existing ctx deadline and
			// this one, so a tighter caller budget always wins.
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, dl)
			defer cancel()
		}
		return next(ctx, req)
	}
}

func (p *Plugin) inboundDeadline(req *gateway.Request) (time.Time, bool) {
	s := req.Header.Get(Header)
	if s == "" {
		return time.Time{}, false
	}
	ns, err := strconv.ParseInt(s, 10, 64)
	if err != nil || ns <= 0 {
		return time.Time{}, false
	}
	return time.Unix(0, ns), true
}
