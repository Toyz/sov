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
	// Tokens are ADDITIONAL accepted join tokens — the previous token(s) during
	// a rotation. A pod passes if its token matches Token OR any of Tokens,
	// enabling make-before-break rollover: issue a new Token while the registry
	// still accepts the old one, then drop the old once every pod has migrated.
	Tokens [][]byte
}

// Plugin is the token-gate plugin returned by New.
type Plugin struct{ tokens [][]byte }

// New returns the registertoken plugin from cfg.
func New(cfgs ...Config) *Plugin {
	if len(cfgs) > 1 {
		panic("registertoken.New: at most one Config")
	}
	var cfg Config
	if len(cfgs) == 1 {
		cfg = cfgs[0]
	}
	var tokens [][]byte
	if len(cfg.Token) > 0 {
		tokens = append(tokens, cfg.Token)
	}
	for _, t := range cfg.Tokens {
		if len(t) > 0 {
			tokens = append(tokens, t)
		}
	}
	return &Plugin{tokens: tokens}
}

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
	if len(p.tokens) == 0 {
		return nil
	}
	presented := []byte(req.Header.Get(proto.RegisterTokenHeader))
	for _, tok := range p.tokens {
		if proto.Verify(tok, presented) {
			return nil
		}
	}
	return rpc.Unauthorized("_register: missing or invalid %s", proto.RegisterTokenHeader)
}
