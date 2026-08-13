package rpc

import "reflect"

// RouterInfo is a registered router's filterable identity, passed to the
// predicate given to Select. Value is the LIVE instance (the pointer as
// registered), so a predicate can match on the wire Name, the Go TypeName,
// OR a type assertion to a marker interface — the registry is queryable by
// name or by capability, whichever the consumer needs.
//
// A router registered purely via rpc.Handle has no receiver struct: Value is
// nil and TypeName is "". It still appears in Select/Find (it IS a registered
// router, served over /rpc), so name/type predicates see it; a capability
// assertion on a nil Value is false, so Implements[T] correctly excludes it.
type RouterInfo struct {
	Name     string // wire name — the /rpc/{router} dispatch segment
	TypeName string // Go struct type name; "" for a Handle-only router
	Value    any    // the registered instance; nil for a Handle-only router
}

// Select returns every registered router whose RouterInfo satisfies pred, in
// registration order. It is the registry's query seam: instead of a consumer
// hard-wiring a list of router names, a plugin ASKS the engine for the routers
// it cares about — by name suffix, Go type name, a marker-interface assertion
// on Value, whatever pred tests.
//
// The mechanism is general, not MCP-specific: the MCP built-in Selects the
// routers that expose tools, but any plugin can Select the routers it wants and
// wire them to its own hooks. A nil pred matches nothing. Read-only snapshot —
// safe to call concurrently with dispatch.
func (e *Engine) Select(pred func(RouterInfo) bool) []RouterInfo {
	if pred == nil {
		return nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []RouterInfo
	for _, name := range e.routerOrder {
		ri, ok := e.routerInfoLocked(name)
		if !ok {
			continue
		}
		if pred(ri) {
			out = append(out, ri)
		}
	}
	return out
}

// RouterValue returns the live instance registered under a router's wire name,
// or ok=false when no router by that name is registered. O(1) lookup — the
// single-router counterpart to Select/Find, used when a caller already knows the
// name and wants to type-assert the instance to a marker interface (e.g. a
// surface checking whether a router opts into it).
func (e *Engine) RouterValue(name string) (any, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	rv, ok := e.receiverLocked(name)
	if !ok {
		return nil, false
	}
	return rv.Interface(), true
}

// routerInfoLocked builds the RouterInfo for a registered router name. ok=false
// only when the name is not a registered router at all. A registered router with
// no receiver (Handle-only) still yields a RouterInfo — Name set, TypeName ""
// and Value nil — so it participates in name/type queries and surface tagging
// even though it can't satisfy a capability assertion. Caller holds e.mu (read).
func (e *Engine) routerInfoLocked(name string) (RouterInfo, bool) {
	methods, ok := e.routers[name]
	if !ok || len(methods) == 0 {
		return RouterInfo{}, false
	}
	ri := RouterInfo{Name: name}
	if rv, hasReceiver := e.receiverLocked(name); hasReceiver {
		rt := rv.Type()
		if rt.Kind() == reflect.Ptr {
			rt = rt.Elem()
		}
		ri.TypeName = rt.Name()
		ri.Value = rv.Interface()
	}
	return ri, true
}

// receiverLocked recovers the registered receiver value for a router. A router
// registered reflectively (Engine.Register) carries the same receiver on every
// methodEntry, but a router registered purely via rpc.Handle has typed closures
// with NO receiver struct (me.router is the zero Value). Skip those and return
// the first VALID receiver; if none exists — a Handle-only router — return
// ok=false so callers get a nil Value (RouterValue reports ok=false;
// routerInfoLocked yields a receiver-less RouterInfo) instead of panicking on a
// zero reflect.Value. Caller holds e.mu (read).
func (e *Engine) receiverLocked(name string) (reflect.Value, bool) {
	for _, me := range e.routers[name] {
		if me.router.IsValid() {
			return me.router, true
		}
	}
	return reflect.Value{}, false
}
