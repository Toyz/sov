// Package requestid stamps a stable X-Sov-Request-Id on every
// request as it enters the gateway and propagates it through every
// hop the request takes: outbound proxy calls, in-process local
// dispatch, and downstream gateways (via the same header).
//
// Three hats so the id reaches every dispatch shape:
//   - HeaderParser — generates the id at the inbound edge if no
//     upstream caller set one. Idempotent: existing X-Sov-Request-Id
//     headers pass through unchanged.
//   - HeaderInjector — copies the id onto every outbound proxy
//     request so remote pods see the same id.
//   - ContextContributor — stashes the id on rc.State so in-process
//     handlers see the same id (PEMM symmetry — monolith dispatch
//     gets identical observability to mesh dispatch).
//
// Handlers read the id via ctx.Get(requestid.ContextKey) or the
// helper requestid.FromContext(ctx).
//
//	gw.Use(requestid.New())
//
// Wire as the FIRST HeaderParser so every downstream parser /
// translator / dispatch site already sees the id.
package requestid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

// Header is the wire name. Stable, lowercase-canonicalized by net/http.
const Header = "X-Sov-Request-Id"

// ContextKey is the rc.State key handlers read to pull the id.
const ContextKey = "sov.requestid"

// Config configures the request-id plugin. Generator overrides the
// default 16-byte random hex id (use UUIDv7 / ULID / snowflake by
// supplying a func() string). Nil generator falls back to the default.
type Config struct {
	Generator func() string
}

// Plugin is the request-id propagator returned by New.
type Plugin struct {
	generate func() string
}

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin              = (*Plugin)(nil)
	_ gateway.PluginDoc           = (*Plugin)(nil)
	_ gateway.HeaderClaimer       = (*Plugin)(nil)
	_ gateway.HeaderParser        = (*Plugin)(nil)
	_ gateway.HeaderInjector      = (*Plugin)(nil)
	_ gateway.ContextContributor  = (*Plugin)(nil)
	_ gateway.ResponseInterceptor = (*Plugin)(nil)
	_ gateway.CapabilityProvider  = (*Plugin)(nil)
)

// New returns the request-id plugin from cfg.
func New(cfg Config) *Plugin {
	gen := cfg.Generator
	if gen == nil {
		gen = defaultGenerate
	}
	return &Plugin{generate: gen}
}

// PluginName surfaces in /rpc/_introspect.plugins[].
func (p *Plugin) PluginName() string { return "request-id" }

// Doc satisfies gateway.PluginDoc.
func (p *Plugin) Doc() string {
	return "Generates X-Sov-Request-Id when missing, propagates an upstream-supplied id end-to-end, stamps response header. Anchors request tracing across hops."
}

// ClaimedHeaders declares X-Sov-Request-Id so an upstream-stamped id
// propagates through the edge strip (server-to-server tracing).
func (p *Plugin) ClaimedHeaders() []string { return []string{Header} }

// ParseHeaders generates a fresh id when the inbound request lacks
// one. Existing headers pass through untouched so upstream-supplied
// ids (load balancer, edge gateway, browser tooling) propagate
// end-to-end.
func (p *Plugin) ParseHeaders(req *gateway.Request) *rpc.Error {
	if req.Header.Get(Header) != "" {
		return nil
	}
	if req.Header == nil {
		req.Header = gateway.Header{}
	}
	req.Header[Header] = p.generate()
	return nil
}

// InjectHeaders copies the id onto every outbound proxy hop.
func (p *Plugin) InjectHeaders(_ context.Context, req *gateway.Request, hreq *http.Request) error {
	if id := req.Header.Get(Header); id != "" {
		hreq.Header.Set(Header, id)
	}
	return nil
}

// ContributeContext stashes the id on rc.State so in-process handlers
// see it via FromContext.
func (p *Plugin) ContributeContext(rc *rpc.Context, req *gateway.Request) error {
	if id := req.Header.Get(Header); id != "" {
		rc.Set(ContextKey, id)
	}
	return nil
}

// InterceptResponse stamps the id on the response so clients can
// correlate logs by reading X-Sov-Request-Id off the envelope.
func (p *Plugin) InterceptResponse(req *gateway.Request, resp *gateway.Response) error {
	id := req.Header.Get(Header)
	if id == "" {
		return nil
	}
	if resp.Header == nil {
		resp.Header = gateway.Header{}
	}
	resp.Header[Header] = id
	return nil
}

// IDGenerator is the capability type other plugins consume to mint
// fresh ids without re-implementing the generator. Look up via:
//
//	gen, _ := gateway.GetCapability[requestid.IDGenerator](gw, "requestid.IDGenerator")
//	subID := gen()
type IDGenerator func() string

// Capabilities publishes the plugin's id generator so peers
// (metrics, otlp, audit) can mint correlation ids matching the
// inbound request-id scheme. Same generator instance the plugin
// uses internally.
func (p *Plugin) Capabilities() []gateway.Capability {
	return []gateway.Capability{
		{Type: "requestid.IDGenerator", Impl: IDGenerator(p.generate)},
	}
}

// FromContext returns the request id stashed by ContributeContext, or
// empty string if the plugin isn't wired.
func FromContext(ctx *rpc.Context) string {
	if s, ok := ctx.Get(ContextKey).(string); ok {
		return s
	}
	return ""
}

// defaultGenerate emits 32 hex chars from 16 random bytes. ~2^128
// id space; collision is not a concern at any practical scale.
func defaultGenerate() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
