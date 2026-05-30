// Package meshsecret ships the HMAC gate for /rpc/_register. Pods
// joining the mesh sign their register POST with the same key the
// registry was constructed with; mismatches and replays (>5 min) get
// 401.
//
//	gw.Use(meshsecret.New(meshsecret.Config{Secret: secret}))
package meshsecret

import (
	"time"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/meshsecret/proto"
	"github.com/Toyz/sov/rpc"
)

// Config configures the meshsecret plugin. Secret is the HMAC key
// shared between the registry and every joining pod. Empty secret
// disables the gate.
type Config struct {
	Secret []byte
}

// Plugin is the HMAC-gate plugin returned by New.
type Plugin struct{ secret []byte }

// New returns the meshsecret plugin from cfg.
func New(cfg Config) *Plugin { return &Plugin{secret: cfg.Secret} }

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin        = (*Plugin)(nil)
	_ gateway.PluginDoc     = (*Plugin)(nil)
	_ gateway.HeaderClaimer = (*Plugin)(nil)
	_ gateway.HeaderParser  = (*Plugin)(nil)
)

// PluginName surfaces in /rpc/_introspect.plugins[].
func (p *Plugin) PluginName() string { return "mesh-secret" }

// Doc surfaces a one-line description in /rpc/_introspect + the explorer.
func (p *Plugin) Doc() string {
	return "Gates /rpc/_register with an HMAC signature so only secret-holding pods can join the mesh."
}

// ClaimedHeaders declares the X-Sov-Register-* signature pair so the
// edge-strip preserves them (otherwise the X-Sov- prefix strip nukes
// them before ParseHeaders fires).
func (p *Plugin) ClaimedHeaders() []string {
	return []string{proto.RegisterSigHeader, proto.RegisterTsHeader}
}

// ParseHeaders intercepts /rpc/_register and verifies the
// X-Sov-Register-Sig header against the request body. Other paths
// pass through untouched.
func (p *Plugin) ParseHeaders(req *gateway.Request) *rpc.Error {
	if req.Path != "/rpc/_register" {
		return nil
	}
	if len(p.secret) == 0 {
		return nil
	}
	sig := req.Header.Get(proto.RegisterSigHeader)
	ts := req.Header.Get(proto.RegisterTsHeader)
	if err := proto.Verify(p.secret, sig, ts, req.Body, time.Now()); err != nil {
		return rpc.Unauthorized("_register: %v", err)
	}
	return nil
}
