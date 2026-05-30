package gateway

// LookupCapability returns the first-registered capability with the
// given Type, or (nil, false) when none. Most callers should use the
// generic GetCapability[T] for type-safe access; LookupCapability is
// here for introspection-style code that needs the raw any.
func (g *Gateway) LookupCapability(typeName string) (any, bool) {
	g.muPlugins.RLock()
	defer g.muPlugins.RUnlock()
	for _, e := range g.plugins {
		for _, c := range e.capabilities {
			if c.Type == typeName {
				return c.Impl, true
			}
		}
	}
	return nil, false
}

// AllCapabilities returns every registered capability grouped by
// Type — useful for diagnostics that surface collisions (more than
// one plugin publishing the same Type). Order of entries within a
// slice matches registration order.
func (g *Gateway) AllCapabilities() map[string][]Capability {
	g.muPlugins.RLock()
	defer g.muPlugins.RUnlock()
	out := map[string][]Capability{}
	for _, e := range g.plugins {
		for _, c := range e.capabilities {
			out[c.Type] = append(out[c.Type], c)
		}
	}
	return out
}

// GetCapability is the generic helper consumers reach for. Returns
// the typed value when the capability exists AND the stored Impl
// matches the requested type; (zero, false) otherwise.
//
//	gen, ok := gateway.GetCapability[func() string](gw, "requestid.IDGenerator")
//	if ok {
//	    id := gen()
//	    ...
//	}
//
// Defined as a package-level generic (not a method) because Go
// methods don't take type parameters. The pattern matches the
// stdlib's slog.Any/AttrFunc style.
func GetCapability[T any](g *Gateway, typeName string) (T, bool) {
	raw, ok := g.LookupCapability(typeName)
	if !ok {
		var zero T
		return zero, false
	}
	typed, ok := raw.(T)
	if !ok {
		var zero T
		return zero, false
	}
	return typed, true
}
