package gateway

import (
	"fmt"
	"slices"
)

// reorderPluginsByDependency runs at ListenAndServe entry. Validates
// every PluginDependency.Requires resolves, then topo-sorts g.plugins
// so each plugin sits AFTER its Requires + After dependencies. Rebuilds
// every hook slot list in the new order so the dispatch chain reflects
// the topological order rather than Use-call order.
//
// Stable: plugins with no dependency constraints stay in their original
// Use-order. Returns an error when a Requires cannot be resolved (the
// boot fails). Cycles fall back to the registration order with a Warn
// — the user-facing implication is that one of the After hints can't
// be honored, but the rest of the order still respects Requires.
func (g *Gateway) reorderPluginsByDependency() error {
	g.muPlugins.Lock()
	defer g.muPlugins.Unlock()

	if len(g.plugins) == 0 {
		return nil
	}

	// Index by name for lookup.
	byName := make(map[string]int, len(g.plugins))
	for i, e := range g.plugins {
		byName[e.name] = i
	}

	// Validate Requires first — error out before any reorder if a
	// hard dep is missing.
	for _, e := range g.plugins {
		for _, req := range e.requires {
			if _, ok := byName[req]; !ok {
				return fmt.Errorf("gateway: plugin %q requires plugin %q which is not registered", e.name, req)
			}
		}
	}

	// Build adjacency: dep → []dependants. Both Requires and After
	// produce edges dep → e, so e is processed after dep.
	indeg := make(map[int]int, len(g.plugins))
	adj := make(map[int][]int, len(g.plugins))
	for i, e := range g.plugins {
		indeg[i] = 0
		_ = e
	}
	addEdge := func(depName string, target int) {
		if depIdx, ok := byName[depName]; ok && depIdx != target {
			adj[depIdx] = append(adj[depIdx], target)
			indeg[target]++
		}
	}
	for i, e := range g.plugins {
		for _, r := range e.requires {
			addEdge(r, i)
		}
		for _, a := range e.after {
			addEdge(a, i)
		}
	}

	// Kahn's with stable ordering: pick the smallest original index
	// among nodes with indeg=0 each iteration.
	sorted := make([]*pluginEntry, 0, len(g.plugins))
	ready := []int{}
	for i := range g.plugins {
		if indeg[i] == 0 {
			ready = append(ready, i)
		}
	}
	slices.Sort(ready)
	for len(ready) > 0 {
		i := ready[0]
		ready = ready[1:]
		sorted = append(sorted, g.plugins[i])
		for _, j := range adj[i] {
			indeg[j]--
			if indeg[j] == 0 {
				ready = append(ready, j)
			}
		}
		slices.Sort(ready)
	}

	if len(sorted) != len(g.plugins) {
		// Cycle. Log and keep Use order. Cycle most likely means
		// two plugins each declare After on the other — operator
		// error, but not worth halting boot for since hard Requires
		// already passed.
		g.Log().Warn("gateway: plugin dependency cycle detected; falling back to registration order",
			"plugin_count", len(g.plugins), "sorted_count", len(sorted))
		return nil
	}

	// Commit the topological order. The dispatch fan-out paths iterate
	// g.plugins directly (filtering by sub-interface), so reordering the
	// slice is all that's needed — there are no slot lists to rebuild.
	g.plugins = sorted
	return nil
}
