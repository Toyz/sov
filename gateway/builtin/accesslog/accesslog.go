// Package accesslog emits one structured log line per dispatched call via the
// gateway's Logger. Nothing on sov's request path logged before this: the
// request id the requestid builtin threads onto the response had no server-side
// line to correlate against. accesslog closes that — every call (business,
// framework, denied) produces a line carrying method, status, duration, subject,
// source IP, request id, PEMM mode, and error code.
//
//	gw.Use(accesslog.New())                                  // log every call
//	gw.Use(accesslog.New(accesslog.Config{SampleSuccess: 20})) // 1-in-20 of 2xx; all >=400
//
// Log level tracks status: >=500 Error, >=400 Warn, else Info. It reads the
// DispatchEvent that audit/metrics already consume, so it also sees auth
// failures and authz denials (recorded since the denial-recording fix).
package accesslog

import (
	"sync/atomic"

	"github.com/Toyz/sov/gateway"
)

// Config configures accesslog. SampleSuccess, when > 1, logs only 1-in-N
// successful (status < 400) calls; errors (>= 400) are always logged. 0 or 1
// logs every call.
type Config struct {
	SampleSuccess int
}

// Plugin is the access-logger returned by New.
type Plugin struct {
	gw     *gateway.Gateway
	sample uint64
	n      atomic.Uint64
}

// New returns an accesslog plugin from cfg.
func New(cfgs ...Config) *Plugin {
	if len(cfgs) > 1 {
		panic("accesslog.New: at most one Config")
	}
	var cfg Config
	if len(cfgs) == 1 {
		cfg = cfgs[0]
	}
	s := cfg.SampleSuccess
	if s < 1 {
		s = 1
	}
	return &Plugin{sample: uint64(s)}
}

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin        = (*Plugin)(nil)
	_ gateway.PluginDoc     = (*Plugin)(nil)
	_ gateway.ConfigApplier = (*Plugin)(nil)
	_ gateway.DispatchHook  = (*Plugin)(nil)
)

// PluginName surfaces in /rpc/_introspect.plugins[].
func (p *Plugin) PluginName() string { return "accesslog" }

// Doc surfaces a one-line description in /rpc/_introspect + the explorer.
func (p *Plugin) Doc() string {
	return "Logs one structured line per dispatch (method, status, duration, subject, ip, request-id) via the gateway logger."
}

// Apply grabs the gateway pointer so OnDispatch can reach the logger.
func (p *Plugin) Apply(g *gateway.Gateway) error { p.gw = g; return nil }

// OnDispatch logs the event.
func (p *Plugin) OnDispatch(ev gateway.DispatchEvent) error {
	if p.gw == nil {
		return nil
	}
	if ev.Status < 400 && p.sample > 1 && p.n.Add(1)%p.sample != 0 {
		return nil // sampled out
	}
	name := ev.Router + "." + ev.Method
	if ev.Router == "" { // framework endpoint or pre-dispatch reject
		name = ev.Path
	}
	args := []any{"method", name, "status", ev.Status, "dur_ms", ev.Duration.Milliseconds(), "mode", ev.Mode}
	if ev.Subject != "" {
		args = append(args, "subject", ev.Subject)
	}
	if ev.RemoteIP != "" {
		args = append(args, "ip", ev.RemoteIP)
	}
	if ev.RequestID != "" {
		args = append(args, "rid", ev.RequestID)
	}
	if ev.ErrorCode != "" {
		args = append(args, "err", ev.ErrorCode)
	}
	log := p.gw.Log()
	switch {
	case ev.Status >= 500:
		log.Error("rpc", args...)
	case ev.Status >= 400:
		log.Warn("rpc", args...)
	default:
		log.Info("rpc", args...)
	}
	return nil
}
