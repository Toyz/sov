// Package metrics ships a Prometheus-text-format metrics plugin for
// sov. Zero external deps — emits the exposition format by hand.
//
// Hooks satisfied:
//
//   - DispatchHook         increments counter + observes histogram per call
//   - RouteHandler         serves /metrics in Prometheus text format
//   - IntrospectContributor reports current counter snapshot in Extra
//   - CapabilityProvider   publishes metrics.Snapshot for in-process readers
//   - PluginDependency     Requires "request-id" (request-id label only
//     meaningful when ids are minted)
//   - Plugin               canonical name "metrics"
//
// Cardinality is capped per-label-set; once Config.CardinalityCap is
// hit, further unseen label combinations are aggregated under a
// synthetic "_overflow" label value and a warning is logged once via
// the gateway Logger.
package metrics

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Toyz/sov/gateway"
)

// Snapshot is the capability shape published as "metrics.Snapshot".
// Callers (audit dashboards, healthchecks, other plugins) read live
// counters in-process without scraping /metrics.
type Snapshot = func() *MetricsSnapshot

// MetricsSnapshot is a point-in-time copy of every counter + histogram.
type MetricsSnapshot struct {
	Counters   map[string]uint64
	Histograms map[string][]float64 // sorted bucket upper bounds → counts
	At         time.Time
}

// Config tunes the metrics plugin.
type Config struct {
	// Buckets is the histogram upper-bound sequence (seconds). Empty
	// uses the Prom-default 11-bucket set.
	Buckets []float64
	// CardinalityCap caps the number of unique label combinations per
	// metric. 0 uses a default (1024). Excess label-sets coalesce into
	// the synthetic "_overflow" bucket.
	CardinalityCap int
	// ExposePath is the route the /metrics endpoint serves on. Empty
	// defaults to "/metrics".
	ExposePath string
	// Namespace prepends the metric name (e.g. "sov" → sov_requests_total).
	// Empty defaults to "sov".
	Namespace string
}

// Plugin is the metrics plugin.
type Plugin struct {
	cfg Config

	mu           sync.RWMutex
	counter      map[string]uint64    // labelKey → count
	durationHist map[string][]uint64  // labelKey → bucket counts (len(buckets)+1)
	durationSum  map[string]float64   // labelKey → total seconds
	durationCnt  map[string]uint64    // labelKey → sample count
	labels       map[string][6]string // labelKey → (router, method, status, mode, error_code, _ unused)
	overflowed   bool

	logOnce sync.Once
	gw      *gateway.Gateway
}

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin                = (*Plugin)(nil)
	_ gateway.PluginDoc             = (*Plugin)(nil)
	_ gateway.PluginDependency      = (*Plugin)(nil)
	_ gateway.CapabilityProvider    = (*Plugin)(nil)
	_ gateway.DispatchHook          = (*Plugin)(nil)
	_ gateway.RouteHandler          = (*Plugin)(nil)
	_ gateway.IntrospectContributor = (*Plugin)(nil)
	_ gateway.ConfigApplier         = (*Plugin)(nil)
)

// New returns the metrics plugin.
func New(cfg Config) *Plugin {
	if len(cfg.Buckets) == 0 {
		cfg.Buckets = defaultBuckets()
	}
	if cfg.CardinalityCap <= 0 {
		cfg.CardinalityCap = 1024
	}
	if cfg.ExposePath == "" {
		cfg.ExposePath = "/metrics"
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "sov"
	}
	return &Plugin{
		cfg:          cfg,
		counter:      make(map[string]uint64),
		durationHist: make(map[string][]uint64),
		durationSum:  make(map[string]float64),
		durationCnt:  make(map[string]uint64),
		labels:       make(map[string][6]string),
	}
}

// PluginName satisfies gateway.Plugin.
func (p *Plugin) PluginName() string { return "metrics" }

// Doc surfaces a one-line description in /rpc/_introspect + the explorer.
func (p *Plugin) Doc() string {
	return "Counts dispatches by route and mode (local/remote/federated); exposes a metrics snapshot."
}

// Requires satisfies gateway.PluginDependency.
func (p *Plugin) Requires() []string { return []string{"request-id"} }

// After is a soft-ordering hint.
func (p *Plugin) After() []string { return nil }

// Capabilities publishes metrics.Snapshot.
func (p *Plugin) Capabilities() []gateway.Capability {
	return []gateway.Capability{
		{Type: "metrics.Snapshot", Impl: Snapshot(p.snapshot)},
	}
}

// OnDispatch updates counters + histogram per request.
func (p *Plugin) OnDispatch(ev gateway.DispatchEvent) error {
	router, method, mode := ev.Router, ev.Method, ev.Mode
	status := strconv.Itoa(ev.Status)
	errCode := ev.ErrorCode

	p.mu.Lock()
	defer p.mu.Unlock()

	key := router + "|" + method + "|" + status + "|" + mode + "|" + errCode
	if _, seen := p.labels[key]; !seen {
		if len(p.labels) >= p.cfg.CardinalityCap {
			key = "_overflow|_overflow|_overflow|_overflow|_overflow"
			p.overflowed = true
			p.maybeLogOverflow()
		}
		if _, seen2 := p.labels[key]; !seen2 {
			p.labels[key] = [6]string{router, method, status, mode, errCode}
			p.durationHist[key] = make([]uint64, len(p.cfg.Buckets)+1)
		}
	}

	p.counter[key]++

	seconds := ev.Duration.Seconds()
	p.durationSum[key] += seconds
	p.durationCnt[key]++
	buckets := p.durationHist[key]
	placed := false
	for i, ub := range p.cfg.Buckets {
		if seconds <= ub {
			buckets[i]++
			placed = true
			break
		}
	}
	if !placed {
		buckets[len(p.cfg.Buckets)]++
	}
	return nil
}

// RoutePatterns satisfies gateway.RouteHandler.
func (p *Plugin) RoutePatterns() []string { return []string{p.cfg.ExposePath} }

// ServeRoute renders Prometheus exposition format.
func (p *Plugin) ServeRoute(_ context.Context, _ *gateway.Request) *gateway.Response {
	var sb strings.Builder
	p.mu.RLock()
	defer p.mu.RUnlock()

	ns := p.cfg.Namespace
	counterName := ns + "_requests_total"
	histName := ns + "_request_duration_seconds"

	fmt.Fprintf(&sb, "# HELP %s Total RPC requests dispatched.\n", counterName)
	fmt.Fprintf(&sb, "# TYPE %s counter\n", counterName)
	keys := sortedKeys(p.counter)
	for _, k := range keys {
		lbls := p.labels[k]
		fmt.Fprintf(&sb, "%s{%s} %d\n", counterName, renderLabels(lbls), p.counter[k])
	}

	fmt.Fprintf(&sb, "# HELP %s RPC request duration in seconds.\n", histName)
	fmt.Fprintf(&sb, "# TYPE %s histogram\n", histName)
	hkeys := sortedKeys(p.durationCnt)
	for _, k := range hkeys {
		lbls := p.labels[k]
		labelStr := renderLabels(lbls)
		buckets := p.durationHist[k]
		var cum uint64
		for i, ub := range p.cfg.Buckets {
			cum += buckets[i]
			fmt.Fprintf(&sb, "%s_bucket{%s,le=\"%s\"} %d\n", histName, labelStr, formatFloat(ub), cum)
		}
		cum += buckets[len(p.cfg.Buckets)]
		fmt.Fprintf(&sb, "%s_bucket{%s,le=\"+Inf\"} %d\n", histName, labelStr, cum)
		fmt.Fprintf(&sb, "%s_sum{%s} %s\n", histName, labelStr, formatFloat(p.durationSum[k]))
		fmt.Fprintf(&sb, "%s_count{%s} %d\n", histName, labelStr, p.durationCnt[k])
	}

	return &gateway.Response{
		Status: 200,
		Header: gateway.Header{"Content-Type": "text/plain; version=0.0.4; charset=utf-8"},
		Body:   []byte(sb.String()),
	}
}

// ContributeIntrospect dumps a compact snapshot into the plugin's
// Extra. Decoration-only; ctx/trace/visited unused.
func (p *Plugin) ContributeIntrospect(_ context.Context, report *gateway.IntrospectReport, _ string, _ []string) error {
	if report == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	var total uint64
	for _, c := range p.counter {
		total += c
	}
	extras := map[string]any{
		"requests_total":  total,
		"label_sets":      len(p.labels),
		"cardinality_cap": p.cfg.CardinalityCap,
		"overflowed":      p.overflowed,
	}
	for i := range report.Plugins {
		if report.Plugins[i].Name == "metrics" {
			if report.Plugins[i].Extra == nil {
				report.Plugins[i].Extra = map[string]any{}
			}
			for k, v := range extras {
				report.Plugins[i].Extra[k] = v
			}
			break
		}
	}
	return nil
}

// Apply binds the gateway pointer for runtime logger lookup.
func (p *Plugin) Apply(g *gateway.Gateway) error { p.gw = g; return nil }

func (p *Plugin) maybeLogOverflow() {
	p.logOnce.Do(func() {
		if p.gw != nil {
			p.gw.Log().Warn("metrics: cardinality cap exceeded; coalescing further label sets",
				"cap", p.cfg.CardinalityCap)
		}
	})
}

func (p *Plugin) snapshot() *MetricsSnapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	c := make(map[string]uint64, len(p.counter))
	for k, v := range p.counter {
		c[k] = v
	}
	h := make(map[string][]float64, len(p.durationHist))
	for k, buckets := range p.durationHist {
		row := make([]float64, len(buckets))
		for i, n := range buckets {
			row[i] = float64(n)
		}
		h[k] = row
	}
	return &MetricsSnapshot{Counters: c, Histograms: h, At: time.Now()}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func renderLabels(lbls [6]string) string {
	return fmt.Sprintf(`router=%q,method=%q,status=%q,mode=%q,error_code=%q`,
		lbls[0], lbls[1], lbls[2], lbls[3], lbls[4])
}

func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func defaultBuckets() []float64 {
	return []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
}
