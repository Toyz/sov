// Package configdump is the opt-in plugin that exposes /rpc/_config — a
// sanitized dump of the gateway's effective runtime configuration (load-shed
// cap, circuit-breaker settings, auth/authz wiring, advertise URL, the set of
// wired plugins, and the locally-served routers).
//
// OFF by default: the report carries no secrets (see gateway.ConfigReport),
// but it still discloses operational topology, so exposing it is a deliberate
// choice — appropriate for an internal ops/debug surface, not an internet edge:
//
//	gw.Use(configdump.New())
//
// The report is always buildable in-process via gateway.ConfigReportBody, so an
// operator UI can render it without opening the public endpoint.
package configdump

import "github.com/Toyz/sov/gateway"

// Plugin exposes /rpc/_config. Stateless — its only effect is flipping the
// gateway's config-exposed flag at Apply time.
type Plugin struct{}

// Compile-time proof of the hooks this plugin binds.
var (
	_ gateway.Plugin        = (*Plugin)(nil)
	_ gateway.PluginDoc     = (*Plugin)(nil)
	_ gateway.ConfigApplier = (*Plugin)(nil)
)

// New returns the configdump plugin. Use it to open /rpc/_config:
//
//	gw.Use(configdump.New())
func New() *Plugin { return &Plugin{} }

// PluginName surfaces in /rpc/_introspect.plugins[] and /rpc/_config.plugins[].
func (p *Plugin) PluginName() string { return "configdump" }

// Doc surfaces a one-line description in the introspect report + explorer.
func (p *Plugin) Doc() string {
	return "Exposes the opt-in /rpc/_config sanitized runtime-config dump (no secrets; discloses operational topology)."
}

// Apply opens the endpoint on the gateway. The report-building logic lives in
// the gateway itself; this plugin only flips it from closed to open.
func (p *Plugin) Apply(g *gateway.Gateway) error {
	g.ExposeConfig()
	return nil
}
