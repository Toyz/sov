// Package preempt permits a federated _register to take over a
// service name already claimed by a different address. Map keys are
// exact wire-service names; values are the normalized address allowed
// to claim each. One-way (no symmetric grant-back), boot-time only,
// consumed on successful takeover — register the reverse direction
// if you want to flip back later.
//
// Plugin owns the map + the decision via MeshConflictPolicy
// (federation case — Conflict.FederatedAddrs populated). Framework
// holds no preemption state.
//
//	gw.Use(preempt.New(map[string]string{
//	    "Chirp": "http://team-feed-v2:9100",
//	}))
package preempt

import (
	"sync"

	"github.com/Toyz/sov/gateway"
)

// Config configures preempt. Rules maps wire-service name to the
// normalized address allowed to claim it (URL form is normalized at
// construction; invalid URLs panic).
type Config struct {
	Rules map[string]string
}

// Plugin is the federation-preemption owner returned by New.
type Plugin struct {
	mu sync.RWMutex
	m  map[string]string
}

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin             = (*Plugin)(nil)
	_ gateway.PluginDoc          = (*Plugin)(nil)
	_ gateway.MeshConflictPolicy = (*Plugin)(nil)
)

// New returns a federation-preemption plugin from cfg.
func New(cfg Config) *Plugin {
	cp := make(map[string]string, len(cfg.Rules))
	for k, v := range cfg.Rules {
		canon, err := gateway.NormalizeUpstreamURL(v)
		if err != nil {
			panic("preempt.New: " + err.Error())
		}
		cp[k] = canon
	}
	return &Plugin{m: cp}
}

// PluginName surfaces in /rpc/_introspect.plugins[].
func (p *Plugin) PluginName() string { return "federation-preemption" }

// Doc surfaces a one-line description in /rpc/_introspect + the explorer.
func (p *Plugin) Doc() string {
	return "Policy allowing a new address to take over a federated service name."
}

// AllowMeshConflict satisfies gateway.MeshConflictPolicy. Only acts
// on the federation case (Conflict.FederatedAddrs[1] populated);
// role-takeover requests pass through (returns false so other
// MeshConflictPolicy plugins like roletakeover get the chance).
// Returns true iff the plugin's map approves the new address as the
// takeover for svc.
func (p *Plugin) AllowMeshConflict(svc, _ string, c gateway.Conflict) bool {
	if c.Role != 0 {
		return false
	}
	newAddr := c.FederatedAddrs[1]
	if newAddr == "" {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	want, ok := p.m[svc]
	return ok && want == newAddr
}

// ConsumeConflict drops the entry once it has been applied so the new
// owner cannot be replaced again by the same rule. Operator must add
// a new (reverse) entry to flip back. No-op for role-takeover
// conflicts (preempt only owns federation rules).
func (p *Plugin) ConsumeConflict(svc string, c gateway.Conflict) {
	if c.Role != 0 {
		return
	}
	p.mu.Lock()
	delete(p.m, svc)
	p.mu.Unlock()
}
