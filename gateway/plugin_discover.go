package gateway

import "reflect"

// PluginsImplementing returns every registered plugin that satisfies interface T,
// in registration order (after dependency reordering). This is how a subsystem —
// the framework's own dispatch OR a builtin like the explorer — discovers the
// plugins that implement ITS interface, WITHOUT the core cataloguing that
// interface. The plugin owns its interfaces; consumers ask for what they need:
//
//	for _, h := range gateway.PluginsImplementing[HeaderInjector](g) { h.InjectHeaders(...) }
//	for _, x := range gateway.PluginsImplementing[explorer.Extender](gw) { ... }
//
// Results are memoized per T and rebuilt when a plugin is added, so the hot
// dispatch paths pay one type-assert per plugin per T only once. The returned
// slice is shared — treat it read-only.
func PluginsImplementing[T any](g *Gateway) []T {
	rt := reflect.TypeFor[T]()

	// Fast path: a cached slice for T. Guarded by the read lock so it never races
	// a concurrent Use() rebuilding the cache under the write lock.
	g.muPlugins.RLock()
	if g.hookCache != nil {
		if cached, ok := g.hookCache[rt]; ok {
			g.muPlugins.RUnlock()
			return castHooks[T](cached)
		}
	}
	g.muPlugins.RUnlock()

	g.muPlugins.Lock()
	defer g.muPlugins.Unlock()
	if g.hookCache == nil {
		g.hookCache = map[reflect.Type][]any{}
	}
	if cached, ok := g.hookCache[rt]; ok { // another goroutine may have built it
		return castHooks[T](cached)
	}
	out := make([]any, 0)
	for _, e := range g.plugins {
		if _, ok := e.any.(T); ok {
			out = append(out, e.any)
		}
	}
	g.hookCache[rt] = out
	return castHooks[T](out)
}

// castHooks re-types a cached []any (all known to satisfy T) to []T.
func castHooks[T any](vals []any) []T {
	out := make([]T, len(vals))
	for i, v := range vals {
		out[i] = v.(T)
	}
	return out
}

// hookName returns a plugin's own PluginName for logging a fan-out, or a generic
// label when the plugin declares none. The name comes from the plugin, not from
// a core registry.
func hookName(v any) string {
	if n, ok := v.(interface{ PluginName() string }); ok {
		if s := n.PluginName(); s != "" {
			return s
		}
	}
	return "plugin"
}
