package rpc

// Filter is a composable predicate over a registered router — the unit of the
// registry query DSL. A surface builtin composes filters to select exactly the
// routers it serves, by name, Go type, or capability (interface), instead of
// hard-wiring a list or a name convention. Run a filter with Engine.Find:
//
//	// every router that is an MCP tool source but not an internal one
//	eng.Find(rpc.And(
//		rpc.Implements[mcp.ToolRouter](),
//		rpc.Not(rpc.ByName(func(n string) bool { return strings.HasPrefix(n, "_") })),
//	))
//
// Filter has the same underlying type as the Select predicate, so the two APIs
// interoperate: any Filter is a valid argument to Select, and Find is just the
// DSL-named form of Select.
type Filter func(RouterInfo) bool

// ByName matches routers whose wire name satisfies pred.
func ByName(pred func(name string) bool) Filter {
	return func(ri RouterInfo) bool { return pred(ri.Name) }
}

// ByTypeName matches routers whose Go struct type name satisfies pred.
func ByTypeName(pred func(typeName string) bool) Filter {
	return func(ri RouterInfo) bool { return pred(ri.TypeName) }
}

// Implements matches routers that implement interface T — the by-CAPABILITY
// filter. Generic sugar over a type assertion on the live instance, so a
// builtin queries "routers that can do X" without reflect boilerplate:
//
//	eng.Find(rpc.Implements[MyCapability]())
func Implements[T any]() Filter {
	return func(ri RouterInfo) bool { _, ok := ri.Value.(T); return ok }
}

// And matches when every filter matches. Zero filters matches everything.
func And(filters ...Filter) Filter {
	return func(ri RouterInfo) bool {
		for _, f := range filters {
			if !f(ri) {
				return false
			}
		}
		return true
	}
}

// Or matches when any filter matches. Zero filters matches nothing.
func Or(filters ...Filter) Filter {
	return func(ri RouterInfo) bool {
		for _, f := range filters {
			if f(ri) {
				return true
			}
		}
		return false
	}
}

// Not inverts a filter.
func Not(f Filter) Filter {
	return func(ri RouterInfo) bool { return !f(ri) }
}

// Find returns every registered router matching f, in registration order — the
// composable, DSL-named form of Select. A nil filter matches nothing.
func (e *Engine) Find(f Filter) []RouterInfo {
	if f == nil {
		return nil
	}
	return e.Select(f)
}
