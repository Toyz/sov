package gateway

import (
	"fmt"
	"time"
)

// RegisterAuth registers a typed AuthService as the gateway's auth
// verifier AND adds it to the engine. Compile-time check via the
// AuthService interface replaces the prior boot-time panic on
// magic-method-name + signature mismatches.
//
// Equivalent to gw.Register(svc) — the plain Register path auto-detects
// AuthService implementers and binds them. Keep this form when you
// want the intent explicit in main(); use Register when the wiring
// is generic.
func (g *Gateway) RegisterAuth(svc AuthService) {
	g.engine.Register(svc)
	g.autoBindRoles(svc)
}

// RegisterAuthz is the authz equivalent of RegisterAuth.
func (g *Gateway) RegisterAuthz(svc AuthzService) {
	g.engine.Register(svc)
	g.autoBindRoles(svc)
}

// bindAuth records the auth binding, panicking if a different service
// is already bound. The panic fires at boot — fail-fast for
// misconfiguration (two AuthService implementers in one binary).
// Mesh-mode _register has the equivalent 409 ROLE_CONFLICT path; that
// runs after the gateway is serving, hence HTTP not panic.
func (g *Gateway) bindAuth(service, method string) {
	if g.authBinding != nil && g.authBinding.Service != service {
		panic(fmt.Sprintf(
			"gateway: two services satisfy AuthService — %q already bound, %q also implements it; "+
				"remove one or wrap one to break the interface match",
			g.authBinding.Service, service))
	}
	g.authBinding = &AuthBinding{Service: service, Method: method}
}

func (g *Gateway) bindAuthz(service, method string) {
	if g.authzBinding != nil && g.authzBinding.Service != service {
		panic(fmt.Sprintf(
			"gateway: two services satisfy AuthzService — %q already bound, %q also implements it; "+
				"remove one or wrap one to break the interface match",
			g.authzBinding.Service, service))
	}
	g.authzBinding = &AuthzBinding{Service: service, Method: method}
}

// autoBindRoles binds the auth/authz roles a router implements. Shared by
// Register, RegisterAuth/RegisterAuthz, and Use so the AuthService /
// AuthzService detection lives in one place. Strict binding — panics on a
// conflicting in-process role (see bindAuth/bindAuthz).
func (g *Gateway) autoBindRoles(router any) {
	if svc, ok := router.(AuthService); ok {
		g.bindAuth(routerWireName(svc), "verify")
	}
	if svc, ok := router.(AuthzService); ok {
		g.bindAuthz(routerWireName(svc), "check")
	}
}

// RegisterRemote hand-registers a remote service in the resolver chain.
// In-process equivalent of a remote pod POSTing /rpc/_register. ttl is
// how long the entry stays alive without a refresh.
//
// Pass RemoteOptions to bind auth/authz roles AND/OR opt into
// /rpc/_introspect aggregation in the same call — registering an
// auth service remotely now matches the in-process RegisterAuth
// shape so the call site can switch deployment modes without
// touching the registration code:
//
//	// In-process:
//	gw.RegisterAuth(&auth.AuthRouter{...})
//
//	// Remote (mesh):
//	gw.RegisterRemote("Auth", "http://auth-pod:9001", time.Minute,
//	    RemoteOptions{Auth: true, Verify: "verify"})
func (g *Gateway) RegisterRemote(name, address string, ttl time.Duration, opts ...RemoteOptions) {
	var o RemoteOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	g.register.PutEntry(name, address, ttl, EntryOptions{Introspectable: o.Introspect})
	if o.Auth {
		method := o.Verify
		if method == "" {
			method = "verify"
		}
		g.OverrideAuthBinding(name, method)
	}
	if o.Authz {
		method := o.Check
		if method == "" {
			method = "check"
		}
		g.OverrideAuthzBinding(name, method)
	}
}

// RemoteOptions configures RegisterRemote — role flags + introspect
// opt-in, parallel to what an inbound /rpc/_register POST carries.
// Empty zero value preserves the legacy two-arg behavior.
type RemoteOptions struct {
	// Auth, when true, binds (name, Verify) as the gateway's auth
	// verifier. Verify defaults to "verify" if empty.
	Auth   bool
	Verify string
	// Authz, when true, binds (name, Check) as the gateway's authz
	// policy hook. Check defaults to "check" if empty.
	Authz bool
	Check string
	// Introspect, when true, opts this entry into the gateway's
	// /rpc/_introspect aggregator fan-out. Default false (no probe).
	Introspect bool
}

// OverrideAuthBinding records the auth role binding, SILENTLY
// overwriting any existing one — the override semantics are the whole
// point, so the name says so. The registry plugin calls this from its
// /rpc/_register handler AFTER its RoleConflictPolicy check has already
// authorized the takeover; RegisterRemote uses it because remote
// registration is inherently last-writer-wins. The in-process
// RegisterAuth path instead uses the strict, unexported bindAuth, which
// panics on a conflicting binding (fail-fast for two AuthService
// implementers compiled into one binary).
func (g *Gateway) OverrideAuthBinding(service, method string) {
	g.authBinding = &AuthBinding{Service: service, Method: method}
}

// OverrideAuthzBinding is the authz equivalent of OverrideAuthBinding.
func (g *Gateway) OverrideAuthzBinding(service, method string) {
	g.authzBinding = &AuthzBinding{Service: service, Method: method}
}

// AuthBinding returns the current auth binding (nil if unbound).
// Registry plugin reads this to detect cross-name role conflicts on
// /rpc/_register.
func (g *Gateway) AuthBinding() *AuthBinding { return g.authBinding }

// AuthzBinding returns the current authz binding (nil if unbound).
func (g *Gateway) AuthzBinding() *AuthzBinding { return g.authzBinding }
