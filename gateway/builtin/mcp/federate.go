package mcp

import (
	"context"
	"encoding/json"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

// surfaceName is the tag mcp stamps on the routers it exposes as tools. It rides
// the introspect catalog (rpc.RouterDescriptor.Surfaces), so it federates.
const surfaceName = "mcp"

// mcp is an IntrospectContributor: it advertises which routers it exposes as
// tools by tagging them in the introspect report.
var _ gateway.IntrospectContributor = (*Plugin)(nil)

// ContributeIntrospect stamps the "mcp" surface tag onto THIS node's local tool
// routers (those that embed mcp.Tool) in the introspect report. Because the tag
// lives on the RouterDescriptor, it federates: when a parent gateway aggregates
// this node's /rpc/_introspect, it learns which of this node's services are MCP
// tools — even though the mcp.Tool marker is a local Go type that never crosses
// the wire. That is what lets a parent's MCP surface discover, and mesh, tools
// whose services run on another node.
//
// Only LOCAL routers are tagged here; a remote node's services arrive already
// tagged by their own node's mcp plugin, merged in by the registry aggregator.
func (p *Plugin) ContributeIntrospect(_ context.Context, report *gateway.IntrospectReport, _ string, _ []string) error {
	local := map[string]bool{}
	for _, ri := range p.gw.Engine().Find(rpc.Implements[ToolRouter]()) {
		local[ri.Name] = true
	}
	for name, rds := range report.Services {
		if !local[name] {
			continue
		}
		for i := range rds {
			rds[i].Surfaces = appendUnique(rds[i].Surfaces, surfaceName)
		}
	}
	return nil
}

// catalog fetches the FEDERATED introspect report (local engine + every remote
// service the registry aggregator merged in). The public body already strips
// hard-hidden methods and omits soft-internal ones, so what remains is exactly
// the tool-eligible surface.
func (p *Plugin) catalog(ctx context.Context) *gateway.IntrospectReport {
	resp := p.gw.IntrospectBody(ctx, &gateway.Request{Header: gateway.Header{}})
	if resp == nil || resp.Status != 200 {
		return nil
	}
	var rep gateway.IntrospectReport
	if err := json.Unmarshal(resp.Body, &rep); err != nil {
		return nil
	}
	return &rep
}

// hasSurface reports whether rd is tagged with surface s.
func hasSurface(rd rpc.RouterDescriptor, s string) bool {
	for _, x := range rd.Surfaces {
		if x == s {
			return true
		}
	}
	return false
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}
