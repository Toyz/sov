// Package tracing propagates a W3C traceparent across every hop so a mesh
// request forms a real span tree in an OpenTelemetry / Jaeger / Zipkin
// collector — not just one flat correlation id (that is requestid's job).
//
// Three hats, mirroring requestid:
//
//   - HeaderParser — mint a root traceparent at the edge when the inbound
//     request has none (or an invalid one); a valid upstream traceparent is
//     kept as this node's parent span.
//
//   - HeaderInjector — on every OUTBOUND hop, emit a traceparent with the SAME
//     trace-id but a FRESH span-id, so the downstream node's spans nest under
//     this one (parent -> child causality). This is the difference from
//     requestid, which propagates one id unchanged.
//
//   - ContextContributor — stash trace-id + current span-id on ctx for handlers
//     and observability plugins (tracing.TraceIDFromContext).
//
//     gw.Use(tracing.New())  // wire alongside requestid
package tracing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

const (
	traceparentHeader = "traceparent"
	tracestateHeader  = "tracestate"
	ctxTraceID        = "sov.trace.id"
	ctxSpanID         = "sov.trace.span"
)

// Config configures the tracing plugin. A freshly-minted root traceparent is
// marked sampled ("01") by default; set Unsampled to mint it unsampled ("00").
// An inbound traceparent's flags are always preserved regardless.
type Config struct {
	Unsampled bool
}

// Plugin is the traceparent propagator returned by New.
type Plugin struct {
	rootFlags string
}

// New returns the tracing plugin from cfg.
func New(cfgs ...Config) *Plugin {
	if len(cfgs) > 1 {
		panic("tracing.New: at most one Config")
	}
	var cfg Config
	if len(cfgs) == 1 {
		cfg = cfgs[0]
	}
	flags := "01"
	if cfg.Unsampled {
		flags = "00"
	}
	return &Plugin{rootFlags: flags}
}

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin             = (*Plugin)(nil)
	_ gateway.PluginDoc          = (*Plugin)(nil)
	_ gateway.HeaderClaimer      = (*Plugin)(nil)
	_ gateway.HeaderParser       = (*Plugin)(nil)
	_ gateway.HeaderInjector     = (*Plugin)(nil)
	_ gateway.ContextContributor = (*Plugin)(nil)
)

// PluginName surfaces in /rpc/_introspect.plugins[].
func (p *Plugin) PluginName() string { return "tracing" }

// Doc satisfies gateway.PluginDoc.
func (p *Plugin) Doc() string {
	return "Propagates a W3C traceparent across hops, minting a child span per outbound call so the mesh forms a span tree."
}

// ClaimedHeaders declares traceparent + tracestate so they survive the edge
// strip and propagate server-to-server.
func (p *Plugin) ClaimedHeaders() []string { return []string{traceparentHeader, tracestateHeader} }

// ParseHeaders mints a root traceparent when the inbound one is absent/invalid.
func (p *Plugin) ParseHeaders(req *gateway.Request) *rpc.Error {
	if _, _, ok := parseTraceparent(req.Header.Get(traceparentHeader)); ok {
		return nil
	}
	if req.Header == nil {
		req.Header = gateway.Header{}
	}
	req.Header[traceparentHeader] = buildTraceparent(newTraceID(), newSpanID(), p.rootFlags)
	return nil
}

// InjectHeaders emits a child span (same trace, new span-id) on each hop.
func (p *Plugin) InjectHeaders(_ context.Context, req *gateway.Request, hreq *http.Request) error {
	traceID, flags, ok := parseTraceparent(req.Header.Get(traceparentHeader))
	if !ok {
		return nil
	}
	hreq.Header.Set(traceparentHeader, buildTraceparent(traceID, newSpanID(), flags))
	if ts := req.Header.Get(tracestateHeader); ts != "" {
		hreq.Header.Set(tracestateHeader, ts)
	}
	return nil
}

// ContributeContext stashes trace-id + the current span-id on rc.State.
func (p *Plugin) ContributeContext(rc *rpc.Context, req *gateway.Request) error {
	tp := req.Header.Get(traceparentHeader)
	traceID, _, ok := parseTraceparent(tp)
	if !ok {
		return nil
	}
	rc.Set(ctxTraceID, traceID)
	rc.Set(ctxSpanID, strings.Split(tp, "-")[2])
	return nil
}

// TraceIDFromContext returns the propagated trace id, or "" when tracing isn't
// wired.
func TraceIDFromContext(ctx *rpc.Context) string {
	if s, ok := ctx.Get(ctxTraceID).(string); ok {
		return s
	}
	return ""
}

// SpanIDFromContext returns the current span id, or "".
func SpanIDFromContext(ctx *rpc.Context) string {
	if s, ok := ctx.Get(ctxSpanID).(string); ok {
		return s
	}
	return ""
}

func newTraceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:]) // 32 hex chars
}

func newSpanID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:]) // 16 hex chars
}

func buildTraceparent(traceID, spanID, flags string) string {
	return "00-" + traceID + "-" + spanID + "-" + flags
}

// parseTraceparent validates the W3C "version-traceid-spanid-flags" shape and
// returns the trace-id + flags. Length + hex are checked; an all-zero trace or
// span id is rejected (the spec's invalid sentinel).
func parseTraceparent(tp string) (traceID, flags string, ok bool) {
	parts := strings.Split(tp, "-")
	if len(parts) != 4 {
		return "", "", false
	}
	if len(parts[0]) != 2 || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return "", "", false
	}
	for _, part := range parts {
		if !isHex(part) {
			return "", "", false
		}
	}
	if parts[1] == strings.Repeat("0", 32) || parts[2] == strings.Repeat("0", 16) {
		return "", "", false
	}
	return parts[1], parts[3], true
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
