package gateway

import (
	"context"
)

// LinkPeer wires another in-process *Gateway as the dispatch target
// for the listed service names. Calls bypass HTTP entirely — peer's
// Handle is invoked directly on the dispatch path. Nested PEMM in one
// binary: same handler code, different gateway. Folds what the
// retired localpeer plugin did into the core API.
//
//	pub := sov.New()
//	pub.Register(&PublicRouter{})
//	admin := sov.New()
//	admin.Register(&AdminRouter{})
//	pub.LinkPeer(admin, "Admin")
//
// Service names should NOT collide with the host gateway's own
// routers — the LocalResolver wins the chain.
//
// SCOPE — dispatch only, not introspection. A LinkPeer'd peer is a
// dispatch target: /rpc calls (and anything that routes through Dispatch)
// resolve to it. It is NOT merged into this gateway's /rpc/_introspect
// catalog (peerResolver.Introspectables returns nil), so introspection-driven
// surfaces — MCP tools/list, the explorer, upward federation — do NOT see a
// peer's routers. To expose a node's routers as MCP tools ACROSS a boundary,
// use registry/remote federation (JoinMesh): each node runs its own mcp
// builtin and tags its own tool routers, and the tag federates on the
// descriptor. In-process peer introspection merge is a known gap (HELL-295).
func (g *Gateway) LinkPeer(peer *Gateway, services ...string) {
	if peer == nil || len(services) == 0 {
		return
	}
	g.resolverChain.addPlugin(&peerResolver{
		services: append([]string(nil), services...),
		peer:     Handler(peer.Handle),
	})
	g.invalidateCatalog()
}

// peerResolver is the in-process Resolver for LinkPeer. Kept
// unexported — operators wire via gw.LinkPeer, not by constructing it.
type peerResolver struct {
	services []string
	peer     Handler
}

func (p *peerResolver) Resolve(_ context.Context, service string) (*Endpoint, bool) {
	for _, s := range p.services {
		if s == service {
			return &Endpoint{Peer: p.peer}, true
		}
	}
	return nil, false
}

func (p *peerResolver) Services() []string { return p.services }

// Introspectables returns nil: a LinkPeer'd peer is a dispatch target, not an
// introspection source. Merging a peer's tagged catalog in-process (so its
// routers surface as MCP tools/explorer entries) is a known gap — see LinkPeer
// SCOPE and HELL-295. Use registry federation to expose routers across a node
// boundary today.
func (p *peerResolver) Introspectables() []string { return nil }
