package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// ---------------------------------------------------------------------------
// _health
// ---------------------------------------------------------------------------

// HealthReport is the aggregated /_health JSON body.
type HealthReport struct {
	Status    string                   `json:"status"`
	CheckedAt time.Time                `json:"checked_at"`
	Gateway   HealthGateway            `json:"gateway"`
	Services  map[string]HealthService `json:"services"`
}

// HealthGateway is the gateway's own health stanza.
type HealthGateway struct {
	Status string `json:"status"`
}

// HealthService is one service's reported health.
//
// Status taxonomy:
//
//   - healthy   — service responded green.
//   - degraded  — federated team gateway responded 207 (some-but-not-all
//     of its children are healthy); see Children for the
//     per-tier rollup.
//   - unhealthy — gateway/pod reachable but reported failure (5xx).
//   - unknown   — registered but no liveness signal (4xx, no _health).
//   - missing   — network unreachable / connection refused.
type HealthService struct {
	Status   string                   `json:"status"`
	Local    bool                     `json:"local"`
	Source   string                   `json:"source,omitempty"`   // RemoteAddr when not local
	Detail   string                   `json:"detail,omitempty"`   // populated on failure
	Children map[string]HealthService `json:"children,omitempty"` // populated for federated tier rollup
}

// handleHealth builds the LOCAL-only health report (gateway status +
// in-process services). Remote fan-out is performed by registered
// HealthAggregator plugins. ComputeOverallHealthStatus is then run
// to roll up status across local + remote services.
func (g *Gateway) handleHealth(ctx context.Context) *Response {
	out := HealthReport{
		Status:    "healthy",
		CheckedAt: time.Now().UTC(),
		Gateway:   HealthGateway{Status: "healthy"},
		Services:  map[string]HealthService{},
	}
	for _, name := range g.engine.Routers() {
		out.Services[name] = HealthService{Status: "healthy", Local: true}
	}
	g.callHealthAggregators(ctx, &out)
	out.Status = ComputeOverallHealthStatus(out.Services)
	status := http.StatusOK
	switch out.Status {
	case "degraded":
		status = http.StatusMultiStatus // 207
	case "unhealthy":
		status = http.StatusServiceUnavailable // 503
	}
	body, _ := json.Marshal(out)
	return &Response{Status: status, Body: body}
}

// ComputeOverallHealthStatus rolls up per-service health into a single
// status. Exported so plugin health aggregators can reuse the same
// rule the framework applies to its local view.
func ComputeOverallHealthStatus(svc map[string]HealthService) string { return overallStatus(svc) }

func overallStatus(svc map[string]HealthService) string {
	anyBad := false
	allBad := len(svc) > 0
	for _, s := range svc {
		switch s.Status {
		case "unhealthy":
			anyBad = true
		case "healthy":
			allBad = false
		default:
			allBad = false
		}
	}
	if allBad {
		return "unhealthy"
	}
	if anyBad {
		return "degraded"
	}
	return "healthy"
}
