// Plugin invocation helpers. The dispatch hot path calls these tight
// loops; each takes a snapshot of the slot list under the read lock
// so a concurrent Use() never tears the iteration.
//
// Every iterator routes through safeHook (gateway/recovery.go) so a
// panicking plugin can never crash the gateway. Each call site encodes
// its own failure reaction: soft hooks log + continue, boot hooks
// (bootHooks set in recovery.go) halt startup, and request hooks may
// short-circuit via a RespondErr sentinel (gateway/plugin_errors.go).

package gateway

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/Toyz/sov/rpc"
)

// pluginRoute is the internal record of a single RouteHandler binding.
// pattern is the raw string supplied by the plugin; subtree mirrors
// net/http ServeMux's trailing-"/" convention (true → prefix match,
// false → exact match). handler is the plugin's ServeRoute pointer.
type pluginRoute struct {
	pattern string
	subtree bool
	handler func(ctx context.Context, req *Request) *Response
	owner   string
}

// snapshotPlugins returns a clone of g.plugins taken under the read
// lock. The fan-out helpers iterate the clone so a concurrent Use()
// append never tears the iteration. Callers that only need a presence
// check (headerClaimed, hasSealVerifier, …) hold the RLock directly
// instead of cloning.
func (g *Gateway) snapshotPlugins() []*pluginEntry {
	g.muPlugins.RLock()
	defer g.muPlugins.RUnlock()
	return append([]*pluginEntry(nil), g.plugins...)
}

// matches reports whether this route's pattern matches path (subtree = prefix,
// else exact).
func (r pluginRoute) matches(path string) bool {
	if r.subtree {
		return len(path) >= len(r.pattern) && path[:len(r.pattern)] == r.pattern
	}
	return path == r.pattern
}

// longestPluginRoute returns the index into snap of the MOST-SPECIFIC (longest
// pattern) route matching path, or -1. A single zero-alloc scan — this is the
// hot path, hit once per request. Specificity (not registration order) decides,
// so a broad surface like the /rpc builtin ("/rpc/") never shadows a
// more-specific route ("/rpc/_explorer/") or a catch-all SPA ("/").
func longestPluginRoute(snap []pluginRoute, path string) int {
	best := -1
	for i := range snap {
		if snap[i].matches(path) && (best < 0 || len(snap[i].pattern) > len(snap[best].pattern)) {
			best = i
		}
	}
	return best
}

// pluginRoutesExcept returns every route matching path EXCEPT the one at
// exceptIdx, most-specific first. Only used on the RARE path where the longest
// match declined (returned nil) and routing must fall through — so the
// allocation is off the common path. It includes same-length siblings of the
// declined route (two plugins can claim the same pattern), not just strictly
// shorter ones, so the "falls through to the next match" contract holds even for
// a tie.
func pluginRoutesExcept(snap []pluginRoute, path string, exceptIdx int) []pluginRoute {
	var out []pluginRoute
	for i, r := range snap {
		if i != exceptIdx && r.matches(path) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return len(out[i].pattern) > len(out[j].pattern)
	})
	return out
}

// callHeaderInjectors fires every registered HeaderInjector on hreq.
// Soft severity — a panicking injector logs + skips. Subject must
// stay sealable so the request still proceeds.
func (g *Gateway) callHeaderInjectors(ctx context.Context, req *Request, hreq *http.Request) {
	snap := g.snapshotPlugins()
	for _, e := range snap {
		if e.headerInjector == nil {
			continue
		}
		_, _, _ = g.safeHook("HeaderInjector", e.name, func() error {
			return e.headerInjector.InjectHeaders(ctx, req, hreq)
		})
	}
}

// callHeaderParsers fires every registered HeaderParser. Returned
// *rpc.Error is CONTROL FLOW — short-circuits dispatch with that
// response (e.g. cors OPTIONS preflight). Only panics route through
// the recovery handler; returned errors don't.
func (g *Gateway) callHeaderParsers(req *Request) *rpc.Error {
	snap := g.snapshotPlugins()
	for _, e := range snap {
		if e.headerParser == nil {
			continue
		}
		var firstErr *rpc.Error
		_, _, _ = g.safeHook("HeaderParser", e.name, func() error {
			firstErr = e.headerParser.ParseHeaders(req)
			// Return nil so the recovery handler does NOT see the
			// short-circuit as a failure. Only panics propagate.
			return nil
		})
		if firstErr != nil {
			return firstErr
		}
	}
	return nil
}

// callAuthTranslators runs after the auth middleware resolves Claims.
// Soft — translation skipped if plugin panics.
func (g *Gateway) callAuthTranslators(req *Request, claims *Claims) {
	snap := g.snapshotPlugins()
	for _, e := range snap {
		if e.authTranslator == nil {
			continue
		}
		_, _, _ = g.safeHook("AuthTranslator", e.name, func() error {
			return e.authTranslator.TranslateAuth(req, claims)
		})
	}
}

// callDispatchHooks fans a post-handler event to every DispatchHook.
// Soft — hook is post-response; failure can never break the wire.
func (g *Gateway) callDispatchHooks(ev DispatchEvent) {
	snap := g.snapshotPlugins()
	for _, e := range snap {
		if e.dispatchHook == nil {
			continue
		}
		_, _, _ = g.safeHook("DispatchHook", e.name, func() error {
			return e.dispatchHook.OnDispatch(ev)
		})
	}
}

// callBootValidators runs once at ListenAndServe entry. Halt — first
// failure aborts startup with a wrapped error.
func (g *Gateway) callBootValidators() error {
	snap := g.snapshotPlugins()
	for _, e := range snap {
		if e.bootValidator == nil {
			continue
		}
		_, bootErr, _ := g.safeHook("BootValidator", e.name, func() error {
			return e.bootValidator.ValidateBoot(g)
		})
		if bootErr != nil {
			return bootErr
		}
	}
	return nil
}

// callLifecycleStart fires OnStart on every LifecycleHook in
// registration order. Halt — first failure aborts startup.
func (g *Gateway) callLifecycleStart(ctx context.Context) error {
	snap := g.snapshotPlugins()
	for _, e := range snap {
		if e.lifecycleHook == nil {
			continue
		}
		_, bootErr, _ := g.safeHook("LifecycleHook.OnStart", e.name, func() error {
			return e.lifecycleHook.OnStart(ctx)
		})
		if bootErr != nil {
			return bootErr
		}
	}
	return nil
}

// callLifecycleStop fires OnStop in REVERSE order. Soft — shutdown
// is best-effort; we log and keep tearing down.
func (g *Gateway) callLifecycleStop(ctx context.Context) {
	snap := g.snapshotPlugins()
	for i := len(snap) - 1; i >= 0; i-- {
		e := snap[i]
		if e.lifecycleHook == nil {
			continue
		}
		_, _, _ = g.safeHook("LifecycleHook.OnStop", e.name, func() error {
			return e.lifecycleHook.OnStop(ctx)
		})
	}
}

// policyAllowsMeshConflict iterates MeshConflictPolicy plugins; first
// true wins. Soft on panic = treated as deny. Covers both the
// role-takeover and federation-preemption paths via the Conflict
// discriminator on c.
func (g *Gateway) policyAllowsMeshConflict(current, candidate string, c Conflict) bool {
	snap := g.snapshotPlugins()
	for _, e := range snap {
		if e.meshConflict == nil {
			continue
		}
		var allow bool
		_, _, _ = g.safeHook("MeshConflictPolicy.AllowMeshConflict", e.name, func() error {
			allow = e.meshConflict.AllowMeshConflict(current, candidate, c)
			return nil
		})
		if allow {
			return true
		}
	}
	return false
}

// consumeMeshConflict fires ConsumeConflict on EVERY registered
// MeshConflictPolicy so each can drop the rule it owns (one-shot
// preemption map cleanup, audit log). Plugins with nothing to clean
// up no-op.
func (g *Gateway) consumeMeshConflict(name string, c Conflict) {
	snap := g.snapshotPlugins()
	for _, e := range snap {
		if e.meshConflict == nil {
			continue
		}
		_, _, _ = g.safeHook("MeshConflictPolicy.ConsumeConflict", e.name, func() error {
			e.meshConflict.ConsumeConflict(name, c)
			return nil
		})
	}
}

// headerClaimed reports whether ANY registered HeaderClaimer plugin
// has claimed the canonical name. NetHTTPServer's strip consults this
// to preserve plugin-owned headers (e.g. mesh-secret's X-Sov-Register-Sig).
func (g *Gateway) headerClaimed(canonicalName string) bool {
	g.muPlugins.RLock()
	defer g.muPlugins.RUnlock()
	for _, e := range g.plugins {
		for _, h := range e.headerClaims {
			if h == canonicalName {
				return true
			}
		}
	}
	return false
}

func (g *Gateway) upstreamTrusted(headers map[string][]string) bool {
	snap := g.snapshotPlugins()
	for _, e := range snap {
		if e.upstreamTrust == nil {
			continue
		}
		var trust bool
		_, _, _ = g.safeHook("UpstreamTrustPolicy", e.name, func() error {
			trust = e.upstreamTrust.TrustUpstream(headers)
			return nil
		})
		if !trust {
			return false
		}
	}
	return true
}

func (g *Gateway) sealValid(headers map[string][]string) bool {
	snap := g.snapshotPlugins()
	any := false
	for _, e := range snap {
		if e.sealVerifier == nil {
			continue
		}
		any = true
		var ok bool
		_, _, _ = g.safeHook("SealVerifier", e.name, func() error {
			ok = e.sealVerifier.VerifySeal(headers)
			return nil
		})
		if ok {
			return true
		}
	}
	return !any
}

func (g *Gateway) hasSealVerifier() bool {
	g.muPlugins.RLock()
	defer g.muPlugins.RUnlock()
	for _, e := range g.plugins {
		if e.sealVerifier != nil {
			return true
		}
	}
	return false
}

func (g *Gateway) hasUpstreamTrust() bool {
	g.muPlugins.RLock()
	defer g.muPlugins.RUnlock()
	for _, e := range g.plugins {
		if e.upstreamTrust != nil {
			return true
		}
	}
	return false
}

// callResponseInterceptors. Soft — interceptor failure is logged;
// response keeps whatever shape it had before.
func (g *Gateway) callResponseInterceptors(req *Request, resp *Response) {
	snap := g.snapshotPlugins()
	for _, e := range snap {
		if e.respInterceptor == nil {
			continue
		}
		_, _, _ = g.safeHook("ResponseInterceptor", e.name, func() error {
			return e.respInterceptor.InterceptResponse(req, resp)
		})
	}
}

// callContextContributors. Soft — missing metadata is degraded but
// not broken.
func (g *Gateway) callContextContributors(ctx *rpc.Context, req *Request) {
	snap := g.snapshotPlugins()
	for _, e := range snap {
		if e.ctxContributor == nil {
			continue
		}
		_, _, _ = g.safeHook("ContextContributor", e.name, func() error {
			return e.ctxContributor.ContributeContext(ctx, req)
		})
	}
}

// callIntrospectContributors fires every registered
// IntrospectContributor on report. Soft — failed contributor leaves
// the report without that contributor's section / merge.
//
// Replaces the prior callIntrospectAggregators +
// callIntrospectAugments split. Each contributor decides whether to
// decorate (local) or fan out (remote) based on the cascade headers
// it receives.
func (g *Gateway) callIntrospectContributors(ctx context.Context, report *IntrospectReport, trace string, visited []string) {
	snap := g.snapshotPlugins()
	for _, e := range snap {
		if e.introContributor == nil {
			continue
		}
		_, _, _ = g.safeHook("IntrospectContributor", e.name, func() error {
			return e.introContributor.ContributeIntrospect(ctx, report, trace, visited)
		})
	}
}

// callHealthAggregators. Soft — failed aggregator leaves local
// health-only report.
func (g *Gateway) callHealthAggregators(ctx context.Context, report *HealthReport) {
	snap := g.snapshotPlugins()
	for _, e := range snap {
		if e.healthAggregator == nil {
			continue
		}
		_, _, _ = g.safeHook("HealthAggregator", e.name, func() error {
			return e.healthAggregator.AggregateHealth(ctx, report)
		})
	}
}

// recordDispatchEventWithMode builds + fires a DispatchEvent from the
// gateway's dispatch path. The outer handler reads resp.Mode to label
// where the call actually ran.
func (g *Gateway) recordDispatchEventWithMode(router, method, path string, status int, started time.Time, subject, errorCode, batchID, mode string) {
	g.muPlugins.RLock()
	any := false
	for _, e := range g.plugins {
		if e.dispatchHook != nil {
			any = true
			break
		}
	}
	g.muPlugins.RUnlock()
	if !any {
		return
	}
	g.callDispatchHooks(DispatchEvent{
		Router:    router,
		Method:    method,
		Path:      path,
		Status:    status,
		Duration:  time.Since(started),
		Subject:   subject,
		ErrorCode: errorCode,
		BatchID:   batchID,
		Mode:      mode,
		At:        time.Now().UTC(),
	})
}
