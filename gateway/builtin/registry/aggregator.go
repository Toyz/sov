package registry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

// ContributeIntrospect fans out to every registered introspectable
// remote (mesh pods + federated team gateways) and merges their
// descriptors into report.Services. Loop guard: skips any address
// already in visited; appends own probes onto visited for downstream
// cascades. Satisfies gateway.IntrospectContributor.
func (p *Plugin) ContributeIntrospect(ctx context.Context, report *gateway.IntrospectReport, trace string, inboundVisited []string) error {
	if p.gw == nil {
		return nil
	}
	reg := p.gw.RegisterResolver()
	resolver := p.gw.Resolver()
	if reg == nil || resolver == nil {
		return nil
	}

	introspectable := map[string]struct{}{}
	for _, name := range resolver.Introspectables() {
		introspectable[name] = struct{}{}
	}

	var groups []addrBucket
	skippedNotIntrospectable := 0
	for addr, svcs := range reg.AddressGroup() {
		pickable := []string{}
		for _, svc := range svcs {
			if _, hit := report.Services[svc]; hit {
				continue
			}
			if _, ok := introspectable[svc]; !ok {
				skippedNotIntrospectable++
				continue
			}
			pickable = append(pickable, svc)
		}
		if len(pickable) > 0 {
			groups = append(groups, addrBucket{address: addr, services: pickable})
		}
	}
	if len(groups) == 0 {
		if skippedNotIntrospectable > 0 {
			p.gw.Log().Warn("registry.aggregator: remotes registered but none marked Introspectable; federated /rpc/_introspect will be local-only",
				"skipped", skippedNotIntrospectable,
				"hint", "pods set MeshOptions.Introspectable=true or RegisterRequest.Introspect=true to opt in")
		}
		return nil
	}

	visited := map[string]struct{}{}
	for _, addr := range inboundVisited {
		visited[addr] = struct{}{}
	}

	fanoutByAddress(groups,
		func(b addrBucket) (map[string][]rpc.RouterDescriptor, bool) {
			canon, _ := gateway.NormalizeUpstreamURL(b.address)
			if _, cycle := visited[canon]; cycle {
				// Synthesize cycle-skipped descriptors so the uniform
				// merge below records them without a special path.
				out := map[string][]rpc.RouterDescriptor{}
				for _, svc := range b.services {
					out[svc] = []rpc.RouterDescriptor{{Router: svc, Title: svc + " (cycle-skipped)"}}
				}
				return out, true
			}
			return p.fetchRemoteIntrospect(ctx, b.address, trace, append(inboundVisited, canon))
		},
		func(b addrBucket, descriptors map[string][]rpc.RouterDescriptor) {
			for _, svc := range b.services {
				if rds, ok := descriptors[svc]; ok {
					report.Services[svc] = rds
				}
			}
		})
	return nil
}

// addrBucket pairs a remote address with the subset of its services a
// fanout cares about.
type addrBucket struct {
	address  string
	services []string
}

// fanoutByAddress probes every bucket concurrently and merges each
// probe's result under a single lock. probe returns (result, ok); a
// false ok skips the merge for that bucket (probe failed and logged).
// Centralizes the WaitGroup + mutex pattern shared by the introspect
// and health aggregators.
func fanoutByAddress[T any](buckets []addrBucket, probe func(b addrBucket) (T, bool), merge func(b addrBucket, res T)) {
	if len(buckets) == 0 {
		return
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(len(buckets))
	for _, b := range buckets {
		go func(b addrBucket) {
			defer wg.Done()
			res, ok := probe(b)
			if !ok {
				return
			}
			mu.Lock()
			merge(b, res)
			mu.Unlock()
		}(b)
	}
	wg.Wait()
}

// AggregateHealth probes every registered remote address ONCE and
// merges the result into report.Services. Probe results fan out to
// every service name that resolves to the same address — federation
// dedupe applies. Status downgrades to "degraded" if any remote
// returned 207; to "unhealthy" if any returned 503.
func (p *Plugin) AggregateHealth(ctx context.Context, report *gateway.HealthReport) error {
	if p.gw == nil {
		return nil
	}
	reg := p.gw.RegisterResolver()
	if reg == nil {
		return nil
	}
	var jobs []addrBucket
	for addr, svcs := range reg.AddressGroup() {
		filtered := make([]string, 0, len(svcs))
		for _, svc := range svcs {
			if _, hit := report.Services[svc]; !hit {
				filtered = append(filtered, svc)
			}
		}
		if len(filtered) > 0 {
			jobs = append(jobs, addrBucket{address: addr, services: filtered})
		}
	}
	fanoutByAddress(jobs,
		func(b addrBucket) (gateway.HealthService, bool) {
			return p.probeAddressHealth(ctx, b.address), true
		},
		func(b addrBucket, h gateway.HealthService) {
			for _, svc := range b.services {
				report.Services[svc] = h
			}
		})
	return nil
}

// probeAddressHealth probes ONE remote address's /rpc/_health and
// returns a HealthService that captures both the gateway's reachability
// and (when the response is a full HealthReport) the per-child tier
// rollup federation needs.
func (p *Plugin) probeAddressHealth(ctx context.Context, addr string) gateway.HealthService {
	url := strings.TrimRight(addr, "/") + "/rpc/_health"
	hctx, cancel := context.WithTimeout(ctx, p.healthProbeTimeout)
	defer cancel()
	hreq, err := http.NewRequestWithContext(hctx, http.MethodGet, url, nil)
	if err != nil {
		return gateway.HealthService{Status: "unknown", Source: addr}
	}
	resp, err := p.gw.ProxyClient().Do(hreq)
	if err != nil {
		return gateway.HealthService{Status: "missing", Source: addr, Detail: err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var report gateway.HealthReport
	hasReport := json.Unmarshal(body, &report) == nil && len(report.Services) > 0
	switch {
	case resp.StatusCode == http.StatusOK:
		if hasReport {
			return gateway.HealthService{Status: "healthy", Source: addr, Children: report.Services}
		}
		return gateway.HealthService{Status: "healthy", Source: addr}
	case resp.StatusCode == http.StatusMultiStatus:
		hs := gateway.HealthService{Status: "degraded", Source: addr}
		if hasReport {
			hs.Children = report.Services
		}
		return hs
	case resp.StatusCode == http.StatusServiceUnavailable:
		hs := gateway.HealthService{Status: "unhealthy", Source: addr, Detail: resp.Status}
		if hasReport {
			hs.Children = report.Services
		}
		return hs
	case resp.StatusCode >= 500:
		return gateway.HealthService{Status: "unhealthy", Source: addr, Detail: "gateway reachable but reported " + resp.Status}
	case resp.StatusCode >= 400:
		return gateway.HealthService{Status: "unknown", Source: addr, Detail: resp.Status}
	default:
		return gateway.HealthService{Status: "unknown", Source: addr, Detail: resp.Status}
	}
}

// fetchRemoteIntrospect probes addr ONCE and returns the downstream's
// full Services map. Caller filters by the service names it cares
// about. Cascade headers propagate so further-downstream loops
// short-circuit. Failures log via gw.Log().Warn so silent
// federation gaps are visible (was the #1 mystery debug case).
func (p *Plugin) fetchRemoteIntrospect(ctx context.Context, addr, trace string, visited []string) (map[string][]rpc.RouterDescriptor, bool) {
	url := strings.TrimRight(addr, "/") + "/rpc/_introspect"
	ictx, cancel := context.WithTimeout(ctx, p.introspectProbeTimeout)
	defer cancel()
	hreq, err := http.NewRequestWithContext(ictx, http.MethodGet, url, nil)
	if err != nil {
		p.gw.Log().Warn("registry.aggregator: build introspect request failed", "addr", addr, "err", err)
		return nil, false
	}
	if trace != "" {
		hreq.Header.Set(gateway.IntrospectTraceHeader, trace)
	}
	if len(visited) > 0 {
		hreq.Header.Set(gateway.IntrospectVisitedHeader, strings.Join(visited, ","))
	}
	resp, err := p.gw.ProxyClient().Do(hreq)
	if err != nil {
		p.gw.Log().Warn("registry.aggregator: remote introspect probe failed (unreachable?)", "addr", addr, "err", err)
		return nil, false
	}
	if resp.StatusCode >= 400 {
		_ = resp.Body.Close()
		p.gw.Log().Warn("registry.aggregator: remote introspect returned error status", "addr", addr, "status", resp.StatusCode)
		return nil, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		p.gw.Log().Warn("registry.aggregator: read remote introspect body failed", "addr", addr, "err", err)
		return nil, false
	}
	services, err := decodeIntrospectBody(body)
	if err != nil {
		p.gw.Log().Warn("registry.aggregator: decode remote introspect body failed", "addr", addr, "err", err, "bytes", len(body))
		return nil, false
	}
	return services, true
}

// decodeIntrospectBody decodes a remote /rpc/_introspect body into a
// services map. It branches on the JSON SHAPE, not on whether services is
// non-empty: a pod whose every method is hard-hidden returns a VALID
// report with an empty services map (e.g. an authz pod whose only method
// `check` is a framework hook). Treating empty-services as "not a report"
// used to fall through to the bare-array path and fail to decode.
//
//   - `[...]`  → legacy bare []RouterDescriptor shape.
//   - `{...}`  → IntrospectReport object (the normal case); empty services valid.
func decodeIntrospectBody(body []byte) (map[string][]rpc.RouterDescriptor, error) {
	if firstJSONByte(body) == '[' {
		var raw []rpc.RouterDescriptor
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, err
		}
		out := map[string][]rpc.RouterDescriptor{}
		for _, rd := range raw {
			out[rd.Router] = []rpc.RouterDescriptor{rd}
		}
		return out, nil
	}
	var wrapped gateway.IntrospectReport
	if err := json.Unmarshal(body, &wrapped); err != nil {
		return nil, err
	}
	return wrapped.Services, nil
}

// firstJSONByte returns the first non-whitespace byte of b, or 0 if b is
// empty/all whitespace. Used to distinguish a JSON array from an object.
func firstJSONByte(b []byte) byte {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return c
		}
	}
	return 0
}
