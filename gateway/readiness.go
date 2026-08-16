package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Serving state, tracked on Gateway.serving (atomic). Liveness (/rpc/_health)
// is independent of this; readiness (/rpc/_ready) is driven by it.
const (
	servingStarting int32 = iota // constructed, not yet accepting: probes 503
	servingReady                 // lifecycle-started and serving: probes 200
	servingDraining              // ctx cancelled, shutting down: probes 503 so the
	// orchestrator stops sending NEW traffic while in-flight requests drain
)

// ReadinessContributor lets a plugin gate the gateway's readiness. Ready
// returns a non-nil error to report NOT ready (a cache still warming, mesh
// registration not yet confirmed, a downstream dependency unreachable). It is
// consulted on every /rpc/_ready probe and never by /rpc/_health — liveness
// and readiness are deliberately separate signals.
type ReadinessContributor interface {
	Ready(ctx context.Context) error
}

// callReadinessContributors returns the first contributor's not-ready error, or
// nil when all report ready. A panicking contributor is treated as not ready.
func (g *Gateway) callReadinessContributors(ctx context.Context) error {
	g.muPlugins.RLock()
	snap := g.plugins
	g.muPlugins.RUnlock()
	for _, e := range snap {
		if e.readinessContrib == nil {
			continue
		}
		var rerr error
		_, _, failed := g.safeHook("ReadinessContributor", e.name, func() error {
			rerr = e.readinessContrib.Ready(ctx)
			return nil // a not-ready is data, not a hook failure — capture it
		})
		if failed {
			return fmt.Errorf("readiness contributor %q panicked", e.name)
		}
		if rerr != nil {
			return rerr
		}
	}
	return nil
}

// handleReady serves /rpc/_ready — the readiness probe, distinct from the
// /rpc/_health liveness probe. Returns 200 only when the gateway has finished
// lifecycle start, is not draining, and every ReadinessContributor reports
// ready; otherwise 503 with a reason. An orchestrator points its readiness
// gate here so a warming pod (starting) or a shutting-down pod (draining) is
// removed from rotation without being restarted.
func (g *Gateway) handleReady(ctx context.Context) *Response {
	switch g.serving.Load() {
	case servingStarting:
		return notReady("starting")
	case servingDraining:
		return notReady("draining")
	}
	if err := g.callReadinessContributors(ctx); err != nil {
		return notReady(err.Error())
	}
	body, _ := json.Marshal(map[string]string{"status": "ready"})
	return &Response{Status: http.StatusOK, Header: Header{"Content-Type": "application/json"}, Body: body}
}

func notReady(reason string) *Response {
	body, _ := json.Marshal(map[string]string{"status": "not_ready", "reason": reason})
	return &Response{Status: http.StatusServiceUnavailable, Header: Header{"Content-Type": "application/json"}, Body: body}
}
