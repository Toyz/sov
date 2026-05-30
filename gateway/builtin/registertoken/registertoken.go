// Package registertoken ships the simple shared-token gate for
// /rpc/_register. A pod joins by presenting the X-Sov-Register-Token
// header matching the token the registry was constructed with; a missing
// or wrong token gets 401.
//
//	gw.Use(registertoken.New(registertoken.Config{Token: token}))
//
// This is the easy join tier (kubeadm / Consul-gossip ergonomics): the
// pod sets one static header, no HMAC. It's a bearer — replayable, so
// rotate it and keep the mesh network-isolated. For body-bound,
// replay-windowed join proof use the meshsecret plugin instead. Both
// gate WHO may join (control plane), distinct from registry.AllowedNames
// (WHICH names) and the X-Sov-* identity bundle (data plane). The two
// join gates compose: register if every registered gate passes.
package registertoken

import (
	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/registertoken/proto"
	"github.com/Toyz/sov/rpc"
)

// Config configures the registertoken plugin. Token is the shared join
// secret the registry and every joining pod hold. Empty token disables
// the gate (register stays open).
type Config struct {
	Token []byte
}

// Plugin is the token-gate plugin returned by New.
type Plugin struct{ token []byte }

// New returns the registertoken plugin from cfg.
func New(cfg Config) *Plugin { return &Plugin{token: cfg.Token} }

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin        = (*Plugin)(nil)
	_ gateway.PluginDoc     = (*Plugin)(nil)
	_ gateway.HeaderClaimer = (*Plugin)(nil)
	_ gateway.HeaderParser  = (*Plugin)(nil)
)

// PluginName surfaces in /rpc/_introspect.plugins[].
func (p *Plugin) PluginName() string { return "register-token" }

// Doc surfaces a one-line description in /rpc/_introspect + the explorer.
func (p *Plugin) Doc() string {
	return "Gates /rpc/_register with a shared bearer token (simple kubeadm-style join), distinct from the meshsecret HMAC gate."
}

// ClaimedHeaders declares the token header so the edge-strip preserves
// it (otherwise the X-Sov- prefix strip nukes it before ParseHeaders).
func (p *Plugin) ClaimedHeaders() []string {
	return []string{proto.RegisterTokenHeader}
}

// ParseHeaders intercepts /rpc/_register and checks the join token.
// Other paths pass through untouched. An empty configured token leaves
// the gate open.
func (p *Plugin) ParseHeaders(req *gateway.Request) *rpc.Error {
	if req.Path != "/rpc/_register" {
		return nil
	}
	if len(p.token) == 0 {
		return nil
	}
	presented := []byte(req.Header.Get(proto.RegisterTokenHeader))
	if !proto.Verify(p.token, presented) {
		return rpc.Unauthorized("_register: missing or invalid %s", proto.RegisterTokenHeader)
	}
	return nil
}
