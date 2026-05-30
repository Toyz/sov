// Package authz is the chirp demo's policy-as-service. It implements
// gateway.AuthzService — the gateway calls Check on every request
// (including anonymous ones) and authz decides allow/deny/require-auth.
//
// The split this package demonstrates:
//
//   - Identity (subject id) comes from auth via X-Sov-Subject.
//   - Authorization (roles, allowed methods) is THIS package's
//     business. Roles are NOT stamped into the auth token. The RBAC
//     map below is the single source of truth, hot-reloadable in
//     production, and downstream services never see it directly.
package authz

import (
	"strings"
	"sync"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

// AuthzRouter implements gateway.AuthzService. The wire name is "Authz"
// (struct-name minus "Router" suffix). All RBAC helpers are unexported
// so the engine reflects only the actual RPC method (Check) plus the
// PublicMethods marker.
type AuthzRouter struct {
	mu sync.RWMutex
	// publicMethods is keyed by "Service/method" for O(1) lookup.
	publicMethods map[string]struct{}
	// modOnly is the set of "Service/method" pairs that require the "mod" role.
	modOnly map[string]struct{}
	// roles is the demo's RBAC map: subject id → role set.
	roles map[string]map[string]struct{}
}

// grantRole adds role to subject's set. Unexported on purpose — RBAC
// mutation is bootstrap-only in the demo. A production policy server
// would expose an admin RPC for this with its own authz check.
func (r *AuthzRouter) grantRole(subject, role string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.roles[subject] == nil {
		r.roles[subject] = map[string]struct{}{}
	}
	r.roles[subject][role] = struct{}{}
}

// hasRole reports whether subject has role.
func (r *AuthzRouter) hasRole(subject, role string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.roles[subject] == nil {
		return false
	}
	_, ok := r.roles[subject][role]
	return ok
}

// PublicMethods on the AuthzRouter itself — Check is what the gateway
// calls but we don't want a circular gate. Anything else on the router
// is internal-only (the RBAC mutation methods could grow here).
func (r *AuthzRouter) PublicMethods() []string {
	return []string{"check"}
}

// NewAuthzRouter returns a chirp-shaped authz policy:
//
//   - Public methods (no auth required) — see DefaultPublicMethods.
//   - mod-only methods — see DefaultModOnlyMethods. Demo grants "mod"
//     to u_alice; everyone else is "user".
//
// Production wires a real policy engine (OPA, Casbin, custom DB) behind
// the same gateway.AuthzService interface — this in-memory matrix is
// the simplest possible thing that exercises every branch of the
// decision shape.
func NewAuthzRouter() *AuthzRouter {
	r := &AuthzRouter{
		publicMethods: map[string]struct{}{},
		modOnly:       map[string]struct{}{},
		roles:         map[string]map[string]struct{}{},
	}
	for _, m := range DefaultPublicMethods {
		r.publicMethods[m] = struct{}{}
	}
	for _, m := range DefaultModOnlyMethods {
		r.modOnly[m] = struct{}{}
	}
	r.grantRole("u_alice", "mod")
	return r
}

// DefaultPublicMethods is the chirp baseline: everyone (including
// anonymous) can hit these. Sign-up, login, public reads.
var DefaultPublicMethods = []string{
	"Auth/register",
	"Auth/login",
	"Auth/verify",
	"User/register",
	"User/get",
	"Chirp/list",
	"Authz/check",
}

// DefaultModOnlyMethods require the "mod" role.
var DefaultModOnlyMethods = []string{
	"Chirp/delete",
}

// Check is the gateway-facing decision endpoint. Decision matrix:
//
//   - publicMethods → {Allow:true}
//   - anonymous + non-public → {Allow:false, Authenticate:true} → 401
//   - authenticated + modOnly + caller lacks "mod" → {Allow:false, Reason:"mod role required"} → 403
//   - otherwise → {Allow:true}
func (r *AuthzRouter) Check(ctx *rpc.Context, p *gateway.CheckParams) (*gateway.AuthzDecision, error) {
	key := p.Service + "/" + p.Method

	_, isPublic := r.publicMethods[key]
	_, isModOnly := r.modOnly[key]

	if isPublic {
		return &gateway.AuthzDecision{Allow: true}, nil
	}
	if p.Claims == nil || strings.TrimSpace(p.Claims.Subject) == "" {
		return &gateway.AuthzDecision{Allow: false, Authenticate: true, Reason: "authentication required"}, nil
	}
	if isModOnly && !r.hasRole(p.Claims.Subject, "mod") {
		return &gateway.AuthzDecision{Allow: false, Reason: "mod role required"}, nil
	}
	return &gateway.AuthzDecision{Allow: true}, nil
}
