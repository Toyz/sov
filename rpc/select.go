package rpc

import "reflect"

// RouterInfo is a registered router's filterable identity, passed to the
// predicate given to Select. Value is the LIVE instance (the pointer as
// registered), so a predicate can match on the wire Name, the Go TypeName,
// OR a type assertion to a marker interface — the registry is queryable by
// name or by capability, whichever the consumer needs.
type RouterInfo struct {
	Name     string // wire name — the /rpc/{router} dispatch segment
	TypeName string // Go struct type name, e.g. "NoteToolsRouter"
	Value    any    // the registered instance (pointer, as registered)
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

// Bound pairs a registered router's wire name with the instance viewed as T —
// the result element of SelectAs.
type Bound[T any] struct {
	Name   string // wire name — the /rpc/{router} dispatch segment
	Router T      // the registered instance, asserted to T
}

// SelectAs returns every registered router that implements interface T, each as
// T alongside its wire name, in registration order. This is the by-capability
// query — filter the registry by an interface type rather than by name:
//
//	for _, b := range rpc.SelectAs[mcp.ToolRouter](engine) {
//		// b.Name is the wire name; b.Router is the router as mcp.ToolRouter
//	}
//
// T is normally an interface; a concrete T matches only routers of exactly that
// dynamic type. It is a free function, not a method, because Go methods cannot
// take type parameters. Read-only snapshot — safe under concurrent dispatch.
func SelectAs[T any](e *Engine) []Bound[T] {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []Bound[T]
	for _, name := range e.routerOrder {
		inst, ok := e.routerInstanceLocked(name)
		if !ok {
			continue
		}
		if t, ok := inst.(T); ok {
			out = append(out, Bound[T]{Name: name, Router: t})
		}
	}
	return out
}

// routerInfoLocked builds the RouterInfo for a registered router name. Caller
// holds e.mu (read). ok=false when the name is unregistered or carries no
// methods to recover an instance from.
func (e *Engine) routerInfoLocked(name string) (RouterInfo, bool) {
	rv, ok := e.receiverLocked(name)
	if !ok {
		return RouterInfo{}, false
	}
	rt := rv.Type()
	if rt.Kind() == reflect.Ptr {
		rt = rt.Elem()
	}
	return RouterInfo{Name: name, TypeName: rt.Name(), Value: rv.Interface()}, true
}

// routerInstanceLocked returns the live router instance for name. Caller holds
// e.mu (read).
func (e *Engine) routerInstanceLocked(name string) (any, bool) {
	rv, ok := e.receiverLocked(name)
	if !ok {
		return nil, false
	}
	return rv.Interface(), true
}

// receiverLocked recovers the registered receiver value for a router. Every
// methodEntry carries the same receiver, so any one of them serves. Caller
// holds e.mu (read).
func (e *Engine) receiverLocked(name string) (reflect.Value, bool) {
	methods, ok := e.routers[name]
	if !ok || len(methods) == 0 {
		return reflect.Value{}, false
	}
	for _, me := range methods {
		return me.router, true
	}
	return reflect.Value{}, false
}
