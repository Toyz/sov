// Package introspect is the opt-in plugin that exposes the public
// /rpc/_introspect endpoint — the aggregated API catalog (every service,
// method, and type the gateway can route).
//
// The endpoint is OFF by default because the catalog discloses the full
// API surface; exposing it is a deliberate choice:
//
//	gw.Use(introspect.New())
//
// Enable it on gateways whose catalog you intend to publish — dev/staging,
// a registry/master that federates a mesh, or any pod that an upstream
// aggregator or the `sov inspect`/`sov gen`/`sov conform` CLIs probe over
// HTTP. Leave it off on internet-facing edges that should not advertise
// their surface.
//
// The explorer plugin does NOT require this: it builds the same report
// in-process (gateway.IntrospectBody) and renders its own UI, so you can
// run the explorer without opening the raw JSON endpoint — and vice versa.
package introspect

import "github.com/Toyz/sov/gateway"

// Plugin exposes /rpc/_introspect. Stateless — its only effect is flipping
// the gateway's introspect-exposed flag at Apply time.
type Plugin struct{}

// Compile-time proof of the hooks this plugin binds.
var (
	_ gateway.Plugin        = (*Plugin)(nil)
	_ gateway.PluginDoc     = (*Plugin)(nil)
	_ gateway.ConfigApplier = (*Plugin)(nil)
)

// New returns the introspect plugin. Use it to open /rpc/_introspect:
//
//	gw.Use(introspect.New())
func New() *Plugin { return &Plugin{} }

// PluginName surfaces in /rpc/_introspect.plugins[] and the manifest.
func (p *Plugin) PluginName() string { return "introspect" }

// Doc surfaces a one-line description in the introspect report + explorer.
func (p *Plugin) Doc() string {
	return "Exposes the public /rpc/_introspect API catalog (opt-in: discloses the full service/method/type surface)."
}

// Apply opens the endpoint on the gateway. The report-building logic lives
// in the gateway itself; this plugin only flips it from closed to open.
func (p *Plugin) Apply(g *gateway.Gateway) error {
	g.ExposeIntrospect()
	return nil
}
