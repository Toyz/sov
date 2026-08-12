package gateway

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/Toyz/sov/rpc"
)

// Surface-builtin support: shared plumbing so every surface (rpc, mcp, and any
// future one) declares its membership and discovers its routers the same way,
// instead of each hand-rolling it.

// TagSurface stamps surfaceName onto every LOCAL router (in this gateway's
// engine) that satisfies pred, within report.Services. The tag lives on the
// rpc.RouterDescriptor (its Surfaces field), so it FEDERATES: a parent gateway
// that aggregates this node's /rpc/_introspect inherits the tag. A surface
// builtin calls this from ContributeIntrospect so the catalog — local and
// aggregated — records which services speak the surface, and a remote surface
// can discover them (see FederatedCatalog).
//
//	// in the mcp builtin's ContributeIntrospect:
//	g.TagSurface(report, "mcp", rpc.Implements[ToolRouter]())
func (g *Gateway) TagSurface(report *IntrospectReport, surfaceName string, pred func(rpc.RouterInfo) bool) {
	if report == nil || pred == nil {
		return
	}
	local := map[string]bool{}
	for _, ri := range g.engine.Find(pred) {
		local[ri.Name] = true
	}
	for name, rds := range report.Services {
		if !local[name] {
			continue
		}
		for i := range rds {
			rds[i].Surfaces = appendSurface(rds[i].Surfaces, surfaceName)
		}
	}
}

// FederatedCatalog returns the aggregated introspect report — this gateway's
// local engine plus every remote service the registry aggregator merged in — or
// nil on failure. A surface builtin uses it to enumerate the routers it serves
// across the whole mesh, then filters by RouterDescriptor.HasSurface.
func (g *Gateway) FederatedCatalog(ctx context.Context) *IntrospectReport {
	resp := g.IntrospectBody(ctx, &Request{Header: Header{}})
	if resp == nil || resp.Status != 200 {
		return nil
	}
	var rep IntrospectReport
	if err := json.Unmarshal(resp.Body, &rep); err != nil {
		return nil
	}
	return &rep
}

func appendSurface(s []string, v string) []string {
	if slices.Contains(s, v) {
		return s
	}
	return append(s, v)
}
