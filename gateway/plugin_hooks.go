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
	// priority overrides specificity-based ordering (RoutePrioritizer). Higher
	// wins over a longer pattern; default 0. See RouteHandler / RoutePrioritizer.
	priority int
}

// matches reports whether this route's pattern matches path (subtree = prefix,
// else exact).
func (r pluginRoute) matches(path string) bool {
	if r.subtree {
		return len(path) >= len(r.pattern) && path[:len(r.pattern)] == r.pattern
	}
	return path == r.pattern
}

// longestPluginRoute returns the index into snap of the MOST-SPECIFIC route
// matching path, or -1. A single zero-alloc scan — this is the hot path, hit
// once per request. Order is by explicit priority first (RoutePrioritizer), then
// pattern length, so a broad surface like the /rpc builtin ("/rpc/") never
// shadows a more-specific route ("/rpc/_explorer/") or a catch-all SPA ("/") —
// and a plugin can override that with RoutePriority. Registration order is NOT a
// factor except as the tiebreak for fully-equal routes (the earliest wins, since
// moreSpecific is strict).
func longestPluginRoute(snap []pluginRoute, path string) int {
	best := -1
	for i := range snap {
		if snap[i].matches(path) && (best < 0 || moreSpecific(snap[i], snap[best])) {
			best = i
		}
	}
	return best
}

// moreSpecific reports whether route a should win over b: higher priority first,
// then longer pattern. Equal on both → false, so an equal-ranked incumbent (the
// earlier-registered route already held as best) keeps the win.
func moreSpecific(a, b pluginRoute) bool {
	if a.priority != b.priority {
		return a.priority > b.priority
	}
	return len(a.pattern) > len(b.pattern)
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
		return moreSpecific(out[i], out[j])
	})
	return out
}

// callHeaderInjectors fires every registered HeaderInjector on hreq.
// Soft severity — a panicking injector logs + skips. Subject must
// stay sealable so the request still proceeds.
func (g *Gateway) callHeaderInjectors(ctx context.Context, req *Request, hreq *http.Request) {
	for _, h := range PluginsImplementing[HeaderInjector](g) {
		_, _, _ = g.safeHook("HeaderInjector", hookName(h), func() error {
			return h.InjectHeaders(ctx, req, hreq)
		})
	}
}

// callHeaderParsers fires every registered HeaderParser. Returned
// *rpc.Error is CONTROL FLOW — short-circuits dispatch with that
// response (e.g. cors OPTIONS preflight). Only panics route through
// the recovery handler; returned errors don't.
func (g *Gateway) callHeaderParsers(req *Request) *rpc.Error {
	for _, h := range PluginsImplementing[HeaderParser](g) {
		var firstErr *rpc.Error
		_, _, _ = g.safeHook("HeaderParser", hookName(h), func() error {
			firstErr = h.ParseHeaders(req)
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
	for _, h := range PluginsImplementing[AuthTranslator](g) {
		_, _, _ = g.safeHook("AuthTranslator", hookName(h), func() error {
			return h.TranslateAuth(req, claims)
		})
	}
}

// callDispatchHooks fans a post-handler event to every DispatchHook.
// Soft — hook is post-response; failure can never break the wire.
func (g *Gateway) callDispatchHooks(ev DispatchEvent) {
	for _, h := range PluginsImplementing[DispatchHook](g) {
		_, _, _ = g.safeHook("DispatchHook", hookName(h), func() error {
			return h.OnDispatch(ev)
		})
	}
}

// callBootValidators runs once at ListenAndServe entry. Halt — first
// failure aborts startup with a wrapped error.
func (g *Gateway) callBootValidators() error {
	for _, h := range PluginsImplementing[BootValidator](g) {
		_, bootErr, _ := g.safeHook("BootValidator", hookName(h), func() error {
			return h.ValidateBoot(g)
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
	for _, h := range PluginsImplementing[LifecycleHook](g) {
		_, bootErr, _ := g.safeHook("LifecycleHook.OnStart", hookName(h), func() error {
			return h.OnStart(ctx)
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
	hooks := PluginsImplementing[LifecycleHook](g)
	for i := len(hooks) - 1; i >= 0; i-- {
		h := hooks[i]
		_, _, _ = g.safeHook("LifecycleHook.OnStop", hookName(h), func() error {
			return h.OnStop(ctx)
		})
	}
}

// policyAllowsMeshConflict iterates MeshConflictPolicy plugins; first
// true wins. Soft on panic = treated as deny. Covers both the
// role-takeover and federation-preemption paths via the Conflict
// discriminator on c.
func (g *Gateway) policyAllowsMeshConflict(current, candidate string, c Conflict) bool {
	for _, h := range PluginsImplementing[MeshConflictPolicy](g) {
		var allow bool
		_, _, _ = g.safeHook("MeshConflictPolicy.AllowMeshConflict", hookName(h), func() error {
			allow = h.AllowMeshConflict(current, candidate, c)
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
	for _, h := range PluginsImplementing[MeshConflictPolicy](g) {
		_, _, _ = g.safeHook("MeshConflictPolicy.ConsumeConflict", hookName(h), func() error {
			h.ConsumeConflict(name, c)
			return nil
		})
	}
}

// headerClaimed reports whether ANY registered HeaderClaimer plugin
// has claimed the canonical name. NetHTTPServer's strip consults this
// to preserve plugin-owned headers (e.g. mesh-secret's X-Sov-Register-Sig).
func (g *Gateway) headerClaimed(canonicalName string) bool {
	for _, hc := range PluginsImplementing[HeaderClaimer](g) {
		for _, raw := range hc.ClaimedHeaders() {
			if raw != "" && http.CanonicalHeaderKey(raw) == canonicalName {
				return true
			}
		}
	}
	return false
}

func (g *Gateway) upstreamTrusted(headers map[string][]string) bool {
	for _, h := range PluginsImplementing[UpstreamTrustPolicy](g) {
		var trust bool
		_, _, _ = g.safeHook("UpstreamTrustPolicy", hookName(h), func() error {
			trust = h.TrustUpstream(headers)
			return nil
		})
		if !trust {
			return false
		}
	}
	return true
}

func (g *Gateway) sealValid(headers map[string][]string) bool {
	verifiers := PluginsImplementing[SealVerifier](g)
	for _, h := range verifiers {
		var ok bool
		_, _, _ = g.safeHook("SealVerifier", hookName(h), func() error {
			ok = h.VerifySeal(headers)
			return nil
		})
		if ok {
			return true
		}
	}
	return len(verifiers) == 0
}

func (g *Gateway) hasSealVerifier() bool {
	return len(PluginsImplementing[SealVerifier](g)) > 0
}

// callResponseInterceptors. Soft — interceptor failure is logged;
// response keeps whatever shape it had before.
func (g *Gateway) callResponseInterceptors(req *Request, resp *Response) {
	for _, h := range PluginsImplementing[ResponseInterceptor](g) {
		_, _, _ = g.safeHook("ResponseInterceptor", hookName(h), func() error {
			return h.InterceptResponse(req, resp)
		})
	}
}

// callContextContributors. Soft — missing metadata is degraded but
// not broken.
func (g *Gateway) callContextContributors(ctx *rpc.Context, req *Request) {
	for _, h := range PluginsImplementing[ContextContributor](g) {
		_, _, _ = g.safeHook("ContextContributor", hookName(h), func() error {
			return h.ContributeContext(ctx, req)
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
	for _, h := range PluginsImplementing[IntrospectContributor](g) {
		_, _, _ = g.safeHook("IntrospectContributor", hookName(h), func() error {
			return h.ContributeIntrospect(ctx, report, trace, visited)
		})
	}
}

// callHealthAggregators. Soft — failed aggregator leaves local
// health-only report.
func (g *Gateway) callHealthAggregators(ctx context.Context, report *HealthReport) {
	for _, h := range PluginsImplementing[HealthAggregator](g) {
		_, _, _ = g.safeHook("HealthAggregator", hookName(h), func() error {
			return h.AggregateHealth(ctx, report)
		})
	}
}

// recordDispatchEventWithMode builds + fires a DispatchEvent from the
// gateway's dispatch path. The outer handler reads resp.Mode to label
// where the call actually ran.
func (g *Gateway) recordDispatchEventWithMode(router, method, path string, status int, started time.Time, subject, errorCode, batchID, mode, remoteIP, requestID string) {
	if len(PluginsImplementing[DispatchHook](g)) == 0 {
		return
	}
	g.callDispatchHooks(DispatchEvent{
		Router:    router,
		Method:    method,
		Path:      path,
		Status:    status,
		Duration:  time.Since(started),
		Subject:   subject,
		RemoteIP:  remoteIP,
		RequestID: requestID,
		ErrorCode: errorCode,
		BatchID:   batchID,
		Mode:      mode,
		At:        time.Now().UTC(),
	})
}
