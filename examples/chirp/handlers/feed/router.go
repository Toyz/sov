// Package feed assembles a user's timeline by calling UserService for
// the follow graph and ChirpService for the chirps. Cross-service calls
// — the PEMM proof point. In monolith mode they're function calls; in
// mesh mode they'd HTTP through the gateway. Same router code either
// way; only the wired Client implementation changes.
package feed

import (
	"github.com/Toyz/sov/examples/chirp/handlers/chirps"
	"github.com/Toyz/sov/examples/chirp/handlers/users"
	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

// Client is the cross-service surface this router needs from the rest
// of the org. Monolith wires NewClientAdapter(gw.LocalClient()); mesh
// wires NewClientAdapter(gateway.NewClient(gwURL)). Handler code does
// not change between the two — that is the entire reason this
// interface exists.
type Client interface {
	UserFollowing(ctx *rpc.Context, userID string) ([]string, error)
	ChirpsByAuthors(ctx *rpc.Context, authorIDs []string, limit int) ([]*chirps.Chirp, error)
}

// NewClientAdapter wraps a gateway.Client to satisfy feed.Client.
func NewClientAdapter(c gateway.Client) Client { return &clientAdapter{c: c} }

type clientAdapter struct{ c gateway.Client }

func (a *clientAdapter) UserFollowing(ctx *rpc.Context, userID string) ([]string, error) {
	var out users.ListFollowingResult
	if err := a.c.Call(ctx, "User", "listFollowing", &users.ListFollowingParams{UserID: userID}, &out); err != nil {
		return nil, err
	}
	return out.FolloweeIDs, nil
}

func (a *clientAdapter) ChirpsByAuthors(ctx *rpc.Context, ids []string, limit int) ([]*chirps.Chirp, error) {
	var out chirps.ListByAuthorsResult
	if err := a.c.Call(ctx, "Chirp", "listByAuthors", &chirps.ListByAuthorsParams{AuthorIDs: ids, Limit: limit}, &out); err != nil {
		return nil, err
	}
	return out.Chirps, nil
}

// FeedRouter exposes Timeline.
type FeedRouter struct {
	Client Client
}

// PublicMethods is empty — every feed method requires identity (the
// feed only makes sense relative to "who is asking"). Listed here so
// introspection callers can confirm there are no public surfaces.
func (r *FeedRouter) PublicMethods() []string { return nil }

// HardHiddenMethods hard-hides debugDump: it is stripped from EVERY
// introspect payload — the explorer (even with "show internal" on),
// codegen, and federated peers never see it. Only an operator who
// already knows the path can find it.
//
// SECURITY: this is discoverability removal, NOT access control. The
// endpoint stays live and dispatchable; authz governs who may call it.
func (r *FeedRouter) HardHiddenMethods() []string { return []string{"debugDump"} }

// TimelineParams is the request body.
type TimelineParams struct {
	Limit int `json:"limit,omitempty"`
}

// TimelineResult is the response body.
type TimelineResult struct {
	Chirps []*chirps.Chirp `json:"chirps"`
}

// Timeline returns the latest chirps from accounts the caller follows.
// Authentication required; the authz service enforces it (or the
// handler-side RequireSubject if no authz is wired).
func (r *FeedRouter) Timeline(ctx *rpc.Context, p *TimelineParams) (*TimelineResult, error) {
	uid, err := rpc.RequireSubject(ctx)
	if err != nil {
		return nil, err
	}
	followees, err := r.Client.UserFollowing(ctx, uid)
	if err != nil {
		return nil, err
	}
	limit := p.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	// Caller's own chirps appear in their feed too — Twitter convention.
	authors := append([]string{uid}, followees...)
	cs, err := r.Client.ChirpsByAuthors(ctx, authors, limit)
	if err != nil {
		return nil, err
	}
	return &TimelineResult{Chirps: cs}, nil
}

// DebugDumpResult is the operator-only diagnostic payload.
type DebugDumpResult struct {
	Note string `json:"note"`
}

// DebugDump is a hard-hidden operator diagnostic (see HardHiddenMethods):
// absent from the explorer/introspect surface, but still dispatchable by
// a caller who knows the path — subject to normal authz.
func (r *FeedRouter) DebugDump(ctx *rpc.Context) (*DebugDumpResult, error) {
	return &DebugDumpResult{Note: "feed router internal diagnostics"}, nil
}
