// Package roletakeover relaxes the default cross-name role-binding
// guard. By default the gateway returns 409 ROLE_CONFLICT when one
// pod tries to claim the Auth/Authz role another pod already holds;
// with this plugin registered, the most recent _register wins.
// Useful for blue-green deploys where the new auth pod legitimately
// needs to replace the old.
//
// Plugin owns the decision via MeshConflictPolicy (role case —
// Conflict.Role non-zero). Framework holds no role-takeover state.
//
//	gw.Use(roletakeover.New())
package roletakeover

import "github.com/Toyz/sov/gateway"

// Config has no fields yet; reserved for future role-scoped policies
// (e.g. allow takeover only for Auth role, not Authz). Empty struct
// keeps the New(Config{}) call shape uniform across all builtins.
type Config struct{}

// Plugin is the always-allow role-takeover policy returned by New.
type Plugin struct{}

// New returns the role-takeover plugin from cfg.
func New(cfg Config) *Plugin { _ = cfg; return &Plugin{} }

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin             = (*Plugin)(nil)
	_ gateway.PluginDoc          = (*Plugin)(nil)
	_ gateway.MeshConflictPolicy = (*Plugin)(nil)
)

// PluginName surfaces in /rpc/_introspect.plugins[].
func (p *Plugin) PluginName() string { return "role-takeover" }

// Doc surfaces a one-line description in /rpc/_introspect + the explorer.
func (p *Plugin) Doc() string {
	return "Policy allowing a new pod to take over the auth or authz role."
}

// AllowMeshConflict satisfies gateway.MeshConflictPolicy. Returns
// true for the role-takeover case (Conflict.Role != 0); returns false
// for the federation-preemption case so other MeshConflictPolicy
// plugins (e.g. preempt) get the chance. Drop the plugin to restore
// the 409 ROLE_CONFLICT default.
func (p *Plugin) AllowMeshConflict(_, _ string, c gateway.Conflict) bool {
	return c.Role != 0
}

// ConsumeConflict is a no-op — this plugin has no per-conflict state
// to clean up. Required by gateway.MeshConflictPolicy.
func (p *Plugin) ConsumeConflict(_ string, _ gateway.Conflict) {}
