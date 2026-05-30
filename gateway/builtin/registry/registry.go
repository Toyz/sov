// Package registry promotes the gateway to central-registry shape —
// owns POST /rpc/_register and the public top-level /health URL.
// Without this plugin the gateway stays in pod shape: /rpc/_register
// returns 404; the public /health is not routed.
//
// The plugin owns the register handler entirely. It defers both
// cross-name role-binding decisions AND federation conflict decisions
// to registered MeshConflictPolicy plugins (e.g. builtin/roletakeover
// for the role case, builtin/preempt for the federation case). The
// framework still owns /rpc/_introspect aggregation today; that move
// is deferred.
//
//	gw.Use(registry.New())
package registry

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

// Config configures the registry plugin. Zero values fall back to
// 2s/1s probe timeouts. AllowedNames, when non-empty, restricts
// /rpc/_register to the listed wire names — defense against a leaked
// mesh key being used to claim an unexpected role-bound name (folds
// what the old standalone allowlist plugin did).
type Config struct {
	IntrospectProbeTimeout time.Duration
	HealthProbeTimeout     time.Duration
	AllowedNames           []string
}

// ttlMultiplier sets a registration's TTL to heartbeat × 3, so a pod can
// miss up to two heartbeats before the gateway evicts it.
const ttlMultiplier = 3

// Plugin is the registry-shape route owner returned by New.
type Plugin struct {
	gw                     *gateway.Gateway
	introspectProbeTimeout time.Duration
	healthProbeTimeout     time.Duration
	allowed                map[string]struct{}
}

// New returns the registry plugin from cfg.
func New(cfg Config) *Plugin {
	if cfg.IntrospectProbeTimeout <= 0 {
		cfg.IntrospectProbeTimeout = 2 * time.Second
	}
	if cfg.HealthProbeTimeout <= 0 {
		cfg.HealthProbeTimeout = 1 * time.Second
	}
	allow := make(map[string]struct{}, len(cfg.AllowedNames))
	for _, n := range cfg.AllowedNames {
		if n != "" {
			allow[n] = struct{}{}
		}
	}
	return &Plugin{
		introspectProbeTimeout: cfg.IntrospectProbeTimeout,
		healthProbeTimeout:     cfg.HealthProbeTimeout,
		allowed:                allow,
	}
}

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
// (ContributeIntrospect + AggregateHealth live in aggregator.go.)
var (
	_ gateway.Plugin                = (*Plugin)(nil)
	_ gateway.PluginDoc             = (*Plugin)(nil)
	_ gateway.CapabilityProvider    = (*Plugin)(nil)
	_ gateway.ConfigApplier         = (*Plugin)(nil)
	_ gateway.BootValidator         = (*Plugin)(nil)
	_ gateway.RouteHandler          = (*Plugin)(nil)
	_ gateway.IntrospectContributor = (*Plugin)(nil)
	_ gateway.HealthAggregator      = (*Plugin)(nil)
)

// nameAllowed reports whether name passes the AllowedNames gate.
// Empty AllowedNames disables the gate (any non-reserved name OK).
func (p *Plugin) nameAllowed(name string) bool {
	if len(p.allowed) == 0 {
		return true
	}
	_, ok := p.allowed[name]
	return ok
}

// PluginName surfaces in /rpc/_introspect.plugins[].
func (p *Plugin) PluginName() string { return "registry" }

// Doc surfaces a one-line description in /rpc/_introspect + the explorer.
func (p *Plugin) Doc() string {
	return "Owns /rpc/_register plus federation, health, and introspect aggregation across the mesh."
}

// AddressGroupFn is the capability type peers consume to read the
// (address → []service) mapping without reaching into the
// RegisterResolver. Used by metrics + drift to know which addresses
// are live without re-implementing the cache.
type AddressGroupFn func() map[string][]string

// Capabilities publishes the address-group reader.
func (p *Plugin) Capabilities() []gateway.Capability {
	return []gateway.Capability{
		{Type: "registry.AddressGroup", Impl: AddressGroupFn(func() map[string][]string {
			if p.gw == nil {
				return nil
			}
			rr := p.gw.RegisterResolver()
			if rr == nil {
				return nil
			}
			return rr.AddressGroup()
		})},
	}
}

// Apply grabs the gateway pointer for later use in ServeRoute.
func (p *Plugin) Apply(g *gateway.Gateway) error {
	p.gw = g
	return nil
}

// registerGated reports whether /rpc/_register has SOME admission gate:
// an AllowedNames allowlist on this plugin, or a sibling join-gate plugin
// (meshsecret / registertoken) registered on the gateway.
func (p *Plugin) registerGated(g *gateway.Gateway) bool {
	if len(p.allowed) > 0 {
		return true
	}
	for _, pi := range g.PluginInfos() {
		if pi.Name == "mesh-secret" || pi.Name == "register-token" {
			return true
		}
	}
	return false
}

// ValidateBoot yells at boot when /rpc/_register is exposed with NO
// admission gate — any reachable actor could self-register a service and
// receive routed traffic. Warning, not a halt: zero-config dev on an
// isolated network is legitimate, but the open endpoint should never be a
// surprise. Silence it with Registry.AllowedNames, or by registering the
// registertoken / meshsecret join-gate plugin.
func (p *Plugin) ValidateBoot(g *gateway.Gateway) error {
	if !p.registerGated(g) {
		g.Log().Warn("registry: /rpc/_register is OPEN — any reachable actor can self-register a service and receive routed traffic. " +
			"Set a join gate before exposing this gateway on an untrusted network: registertoken.Config{Token:...}, " +
			"meshsecret.Config{Secret:...}, or Registry.AllowedNames.")
	}
	return nil
}

// RoutePatterns claims the registry surface. The /rpc/_register and
// public /health routes are owned entirely; /rpc/_introspect stays
// framework-owned (aggregator move deferred).
func (p *Plugin) RoutePatterns() []string {
	return []string{"/rpc/_register", "/health"}
}

// ServeRoute dispatches the two owned paths. /rpc/_register runs the
// full register handler (role binding + federation conflict + write
// to the resolver). /health serves the public k8s health.
func (p *Plugin) ServeRoute(ctx context.Context, req *gateway.Request) *gateway.Response {
	switch req.Path {
	case "/rpc/_register":
		return p.serveRegister(ctx, req)
	case "/health":
		return p.gw.Handle(ctx, &gateway.Request{
			Method: "GET",
			Path:   "/rpc/_health",
			Header: gateway.Header{},
		})
	}
	return gateway.ErrorResponse(rpc.NotFound("registry: unhandled path %q", req.Path))
}

func (p *Plugin) serveRegister(ctx context.Context, req *gateway.Request) *gateway.Response {
	if req.Method != "POST" {
		return gateway.ErrorResponse(&rpc.Error{Status: 405, Code: "BAD_REQUEST", Message: "method not allowed"})
	}
	reg := p.gw.RegisterResolver()
	if reg == nil {
		return gateway.ErrorResponse(&rpc.Error{Status: 503, Code: "UNAVAILABLE", Message: "register resolver not configured"})
	}
	var rr gateway.RegisterRequest
	if err := json.Unmarshal(req.Body, &rr); err != nil {
		return gateway.ErrorResponse(rpc.BadRequest("invalid body: %v", err))
	}
	if rr.Address == "" {
		return gateway.ErrorResponse(rpc.BadRequest("address required"))
	}
	canonAddr, err := gateway.NormalizeUpstreamURL(rr.Address)
	if err != nil {
		return gateway.ErrorResponse(rpc.BadRequest("address: %v", err))
	}
	hb := rr.HeartbeatInterval
	if hb <= 0 {
		hb = 10
	}
	ttl := time.Duration(hb*ttlMultiplier) * time.Second

	if rr.Federate {
		return p.serveFederated(ctx, &rr, canonAddr, ttl)
	}

	if rr.Name == "" {
		return gateway.ErrorResponse(rpc.BadRequest("name required"))
	}
	if strings.HasPrefix(rr.Name, "_") {
		return gateway.ErrorResponse(rpc.BadRequest("service name may not start with _"))
	}
	if !p.nameAllowed(rr.Name) {
		return gateway.ErrorResponse(rpc.Forbidden("_register: service %q not on allow list", rr.Name))
	}
	if rr.Auth {
		if bind := p.gw.AuthBinding(); bind != nil && bind.Service != rr.Name &&
			!p.gw.PolicyAllowsRoleTakeover(bind.Service, rr.Name, gateway.RoleAuth) {
			return gateway.ErrorResponse(&rpc.Error{
				Status: 409, Code: "ROLE_CONFLICT",
				Message: "auth role already held by " + bind.Service + "; register builtin/roletakeover plugin to allow override",
			})
		}
	}
	if rr.Authz {
		if bind := p.gw.AuthzBinding(); bind != nil && bind.Service != rr.Name &&
			!p.gw.PolicyAllowsRoleTakeover(bind.Service, rr.Name, gateway.RoleAuthz) {
			return gateway.ErrorResponse(&rpc.Error{
				Status: 409, Code: "ROLE_CONFLICT",
				Message: "authz role already held by " + bind.Service + "; register builtin/roletakeover plugin to allow override",
			})
		}
	}
	usedPreemption := false
	if existing, ok := p.gw.Resolver().Resolve(ctx, rr.Name); ok {
		existingCanon, _ := gateway.NormalizeUpstreamURL(existing.RemoteAddr)
		if existingCanon != canonAddr {
			if !p.gw.PreemptFederation(rr.Name, existingCanon, canonAddr) {
				return gateway.ErrorResponse(&rpc.Error{
					Status: 409, Code: "SERVICE_CONFLICT",
					Message: "_register: " + rr.Name + " already registered at " + existingCanon,
				})
			}
			usedPreemption = true
		}
	}

	forceIntrospect := p.gw.PluginByName("explorer") != nil
	introspectable := rr.Introspect || forceIntrospect
	reg.PutEntry(rr.Name, rr.Address, ttl, gateway.EntryOptions{Introspectable: introspectable})
	if usedPreemption {
		p.gw.ConsumeFederationPreemption(rr.Name)
	}

	if rr.Auth {
		method := rr.Verify
		if method == "" {
			method = "verify"
		}
		p.gw.OverrideAuthBinding(rr.Name, method)
	}
	if rr.Authz {
		method := rr.Check
		if method == "" {
			method = "check"
		}
		p.gw.OverrideAuthzBinding(rr.Name, method)
	}

	body, _ := json.Marshal(rpc.SuccessResponse{Data: gateway.RegisterResponse{
		OK: true, TTL: int(ttl.Seconds()), ForceIntrospect: forceIntrospect,
	}})
	return &gateway.Response{Status: 200, Body: body}
}

func (p *Plugin) serveFederated(ctx context.Context, rr *gateway.RegisterRequest, canonAddr string, ttl time.Duration) *gateway.Response {
	if rr.Auth || rr.Authz {
		return gateway.ErrorResponse(rpc.BadRequest("federated gateways cannot hold auth/authz role in v1"))
	}
	if len(rr.Services) == 0 {
		return gateway.ErrorResponse(rpc.BadRequest("federate=true requires non-empty services list"))
	}
	preempted := map[string]bool{}
	for _, svc := range rr.Services {
		if svc == "" || strings.HasPrefix(svc, "_") {
			return gateway.ErrorResponse(rpc.BadRequest("invalid federated service name %q", svc))
		}
		if !p.nameAllowed(svc) {
			return gateway.ErrorResponse(rpc.Forbidden("_register: federated service %q not on allow list", svc))
		}
		if p.gw.Engine().HasRouter(svc) {
			return gateway.ErrorResponse(rpc.Conflict("_register: %q served locally; federation cannot shadow", svc))
		}
		if existing, ok := p.gw.Resolver().Resolve(ctx, svc); ok {
			existingCanon, _ := gateway.NormalizeUpstreamURL(existing.RemoteAddr)
			if existingCanon != canonAddr {
				if !p.gw.PreemptFederation(svc, existingCanon, canonAddr) {
					return gateway.ErrorResponse(&rpc.Error{
						Status: 409, Code: "SERVICE_CONFLICT",
						Message: "_register: " + svc + " already federated by " + existingCanon +
							"; register builtin/preempt plugin to allow takeover",
					})
				}
				preempted[svc] = true
			}
		}
	}
	reg := p.gw.RegisterResolver()
	forceIntrospect := p.gw.PluginByName("explorer") != nil
	introspectable := rr.Introspect || forceIntrospect
	for _, svc := range rr.Services {
		reg.PutEntry(svc, rr.Address, ttl, gateway.EntryOptions{Introspectable: introspectable})
		if preempted[svc] {
			p.gw.ConsumeFederationPreemption(svc)
		}
	}
	body, _ := json.Marshal(rpc.SuccessResponse{Data: gateway.RegisterResponse{
		OK: true, TTL: int(ttl.Seconds()), ForceIntrospect: forceIntrospect,
	}})
	return &gateway.Response{Status: 200, Body: body}
}
