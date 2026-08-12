package mcp

import (
	"context"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

// surfaceName is the tag mcp stamps on the routers it exposes as tools. It rides
// the introspect catalog (rpc.RouterDescriptor.Surfaces), so it federates.
const surfaceName = "mcp"

// mcp is an IntrospectContributor: it advertises which routers it exposes as
// tools by tagging them in the introspect report.
var _ gateway.IntrospectContributor = (*Plugin)(nil)

// ContributeIntrospect tags THIS node's local tool routers (those that embed
// mcp.Tool) with the "mcp" surface. Because the tag lives on the descriptor it
// federates — a parent aggregating this node's /rpc/_introspect learns which
// services are MCP tools even though the mcp.Tool marker never crosses the wire,
// which is what lets a parent's MCP surface discover and mesh remote tools. See
// gateway.TagSurface for the shared mechanism.
func (p *Plugin) ContributeIntrospect(_ context.Context, report *gateway.IntrospectReport, _ string, _ []string) error {
	p.gw.TagSurface(report, surfaceName, rpc.Implements[ToolRouter]())
	return nil
}
