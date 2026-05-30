package rpc

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// Engine holds the registered routers and dispatches incoming requests
// to the right Go method by reflection. Safe for concurrent dispatch.
// Mutations (Register) are expected at boot, not under load.
type Engine struct {
	mu             sync.RWMutex
	routers        map[string]map[string]*methodEntry
	publicList     map[string][]string // router → wire method names declared via PublicMethods()
	hiddenList     map[string][]string // router → SOFT-hidden wire names declared via HiddenMethods()
	hardHiddenList map[string][]string // router → HARD-hidden wire names declared via HardHiddenMethods()
	routerOrder    []string
}

// NewEngine returns an empty Engine.
func NewEngine() *Engine {
	return &Engine{
		routers:        map[string]map[string]*methodEntry{},
		publicList:     map[string][]string{},
		hiddenList:     map[string][]string{},
		hardHiddenList: map[string][]string{},
	}
}

// PublicLister is the optional marker a router implements to declare
// which methods are public (no authentication required). The engine
// reads the list once at Register, exposes it via PublicMethods(router)
// and Describe(), and skips the marker method itself when reflecting
// the RPC surface.
type PublicLister interface {
	PublicMethods() []string
}

// HiddenLister is the optional marker a router implements to SOFT-hide
// methods: the named methods are omitted from the default introspect
// report (and the explorer / codegen / federation) but remain in the
// full payload served under the X-Sov-Introspect-Internal header, so the
// explorer's "show internal" toggle can reveal them. The engine reads the
// list once at Register and skips the marker method when reflecting.
//
// Hiding is discoverability only — the methods stay dispatchable.
type HiddenLister interface {
	HiddenMethods() []string
}

// HardHiddenLister is the optional marker a router implements to
// HARD-hide methods: the named methods are stripped from EVERY introspect
// payload — not even the X-Sov-Introspect-Internal header reveals them.
// Use for endpoints only callers who already know the path should find.
//
// SECURITY: hard-hide removes discoverability, NOT access. The endpoint is
// still live and callable; authz, not hiding, is the access boundary.
type HardHiddenLister interface {
	HardHiddenMethods() []string
}

// reservedMarkerMethods lists Go method names the engine treats as
// framework markers rather than RPC methods. Skipped during reflection.
// Covers the PublicLister marker AND every gateway plugin sub-interface
// hook so a Go type can satisfy both router-shape (RPC methods) and
// plugin-shape (hook methods) in one declaration.
//
// MAINTENANCE: every entry is a method name on a gateway plugin
// sub-interface (gateway/plugin.go) or the PublicLister marker. When you
// add a new plugin hook interface, add its method name(s) here. The
// reflection sanity test TestReservedMarkers_CoverPluginInterfaces in
// gateway/plugin_marker_test.go fails the build if the two drift, so a
// forgotten entry surfaces in CI rather than as a mysterious boot-time
// dispatch error. Kept sorted alphabetically.
var reservedMarkerMethods = map[string]bool{
	"After":                true,
	"AggregateHealth":      true,
	"AllowMeshConflict":    true,
	"Apply":                true,
	"Capabilities":         true,
	"ConsumeConflict":      true,
	"ContributeContext":    true,
	"ContributeIntrospect": true,
	"Doc":                  true,
	"Handle":               true,
	"HandleHookFailure":    true,
	"HardHiddenMethods":    true,
	"HiddenMethods":        true,
	"InjectHeaders":        true,
	"InterceptResponse":    true,
	"Introspectables":      true,
	"ListenAndServe":       true,
	"Logger":               true,
	"OnDispatch":           true,
	"OnStart":              true,
	"OnStop":               true,
	"ParseHeaders":         true,
	"PluginName":           true,
	"PublicMethods":        true,
	"Requires":             true,
	"Resolve":              true,
	"RoutePatterns":        true,
	"ServeRoute":           true,
	"Services":             true,
	"TranslateAuth":        true,
	"TrustUpstream":        true,
	"ValidateBoot":         true,
	"VerifySeal":           true,
	"Wrap":                 true,
}

// IsReservedMarker reports whether name is a framework marker or plugin
// hook method the reflection scanner skips rather than treating as an
// RPC method. Exposed so the gateway package's sanity test can assert
// that every plugin sub-interface method is covered here.
func IsReservedMarker(name string) bool { return reservedMarkerMethods[name] }

// methodEntry describes one registered router method after reflection.
type methodEntry struct {
	method     reflect.Method
	router     reflect.Value
	hasParams  bool
	paramType  reflect.Type // value type (not pointer) of the params struct, if any
	resultType reflect.Type // value type of the non-error return, if any
	goName     string       // Go method name, e.g. "Create"
	wireName   string       // wire name, e.g. "create"
	fieldMap   *FieldMap    // resolved sov-tag layout for paramType; nil when hasParams is false
	// internal / internalHard come from a `_ struct{} `sov:"internal"`` /
	// `sov:"internal,hard"`` sentinel on the params struct (per-method
	// visibility via the tag family). Marker-method declarations are
	// applied router-wide in Describe, not here.
	internal     bool
	internalHard bool
	// invoke, when non-nil, is a typed dispatch closure built at boot by
	// rpc.Handle. Dispatch calls it directly instead of the reflect path —
	// no reflect.Value.Call, no reflect.New. Nil for reflectively-
	// registered methods (which use method/router above).
	invoke func(ctx *Context, body []byte) (int, []byte)
}

// ctxType is *Context; cached once to avoid repeated reflect lookups.
var ctxType = reflect.TypeOf((*Context)(nil))
var errType = reflect.TypeOf((*error)(nil)).Elem()

// Register reflects on the given router pointer and exposes its exported
// methods over the wire. The router type's name minus the "Router"
// suffix is used as the wire namespace.
//
// Accepted method signatures:
//
//	func (r *X) Foo(ctx *rpc.Context) error
//	func (r *X) Foo(ctx *rpc.Context) (T, error)
//	func (r *X) Foo(ctx *rpc.Context, p *Params) error
//	func (r *X) Foo(ctx *rpc.Context, p *Params) (T, error)
//
// Anything else panics at boot — fail fast, never at request time.
func (e *Engine) Register(router any) {
	rv := reflect.ValueOf(router)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		panic("rpc.Engine.Register: router must be a non-nil pointer")
	}
	rt := rv.Type()
	typeName := rt.Elem().Name()
	if typeName == "" {
		panic("rpc.Engine.Register: router type must be named (no anonymous structs)")
	}
	routerName := strings.TrimSuffix(typeName, "Router")
	if routerName == typeName {
		panic(fmt.Sprintf("rpc.Engine.Register: router struct %q must end in 'Router'", typeName))
	}

	methods := map[string]*methodEntry{}
	for i := 0; i < rt.NumMethod(); i++ {
		m := rt.Method(i)
		if !m.IsExported() {
			continue
		}
		if reservedMarkerMethods[m.Name] {
			continue
		}
		entry := buildEntry(typeName, rv, m)
		if entry == nil {
			continue
		}
		methods[entry.wireName] = entry
	}
	if len(methods) == 0 {
		panic(fmt.Sprintf("rpc.Engine.Register: router %q exposed zero RPC methods", typeName))
	}

	var public []string
	if lister, ok := router.(PublicLister); ok {
		public = lister.PublicMethods()
	}
	var hidden []string
	if lister, ok := router.(HiddenLister); ok {
		hidden = lister.HiddenMethods()
	}
	var hardHidden []string
	if lister, ok := router.(HardHiddenLister); ok {
		hardHidden = lister.HardHiddenMethods()
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if _, dup := e.routers[routerName]; dup {
		panic(fmt.Sprintf("rpc.Engine.Register: router %q already registered", routerName))
	}
	e.routers[routerName] = methods
	if len(public) > 0 {
		e.publicList[routerName] = public
	}
	if len(hidden) > 0 {
		e.hiddenList[routerName] = hidden
	}
	if len(hardHidden) > 0 {
		e.hardHiddenList[routerName] = hardHidden
	}
	e.routerOrder = append(e.routerOrder, routerName)
}

// PublicMethods returns the wire method names the router declared via
// the PublicLister marker interface, or nil if the router did not
// declare any. Used by the gateway/authz to default-allow without
// per-line configuration.
func (e *Engine) PublicMethods(router string) []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return copySlice(e.publicList[router])
}

// HiddenMethods returns the SOFT-hidden wire method names the router
// declared via the HiddenLister marker, or nil.
func (e *Engine) HiddenMethods(router string) []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return copySlice(e.hiddenList[router])
}

// HardHiddenMethods returns the HARD-hidden wire method names the router
// declared via the HardHiddenLister marker, or nil.
func (e *Engine) HardHiddenMethods(router string) []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return copySlice(e.hardHiddenList[router])
}

func copySlice(src []string) []string {
	if len(src) == 0 {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

func buildEntry(typeName string, rv reflect.Value, m reflect.Method) *methodEntry {
	mt := m.Type
	numIn := mt.NumIn()
	if numIn < 2 || numIn > 3 {
		panicSig(typeName, m, "method must take (*rpc.Context) or (*rpc.Context, *Params)",
			"check the number of parameters")
	}
	if mt.In(1) != ctxType {
		panicSig(typeName, m, "first param must be *rpc.Context",
			fmt.Sprintf("got %s; common mistake: passing context.Context instead of *rpc.Context", mt.In(1)))
	}

	entry := &methodEntry{
		method:   m,
		router:   rv,
		goName:   m.Name,
		wireName: lowerFirst(m.Name),
	}
	if numIn == 3 {
		pt := mt.In(2)
		if pt.Kind() != reflect.Ptr || pt.Elem().Kind() != reflect.Struct {
			panicSig(typeName, m, "params must be a pointer to a struct",
				fmt.Sprintf("got %s; declare params as `*MyParams` where MyParams is a JSON-tagged struct", pt))
		}
		entry.hasParams = true
		entry.paramType = pt.Elem()
		fm, err := BuildFieldMap(entry.paramType)
		if err != nil {
			panic(fmt.Sprintf("rpc.Engine.Register: %s.%s params %s: %v", typeName, m.Name, entry.paramType, err))
		}
		entry.fieldMap = fm
		entry.internal = fm.Internal
		entry.internalHard = fm.InternalHard
	}

	numOut := mt.NumOut()
	if numOut < 1 || numOut > 2 {
		panicSig(typeName, m, "method must return error or (T, error)",
			fmt.Sprintf("got %d return values; sov supports 1 (error) or 2 ((T, error))", numOut))
	}
	if !mt.Out(numOut - 1).Implements(errType) {
		panicSig(typeName, m, "last return must implement error",
			fmt.Sprintf("got %s; the last return type must be `error`", mt.Out(numOut-1)))
	}
	if numOut == 2 {
		entry.resultType = mt.Out(0)
	}
	return entry
}

// Lookup returns the method entry for router/method, or false.
func (e *Engine) Lookup(router, method string) (*methodEntry, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	methods, ok := e.routers[router]
	if !ok {
		return nil, false
	}
	entry, ok := methods[method]
	return entry, ok
}

// HasRouter reports whether a router by that name is registered.
func (e *Engine) HasRouter(router string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.routers[router]
	return ok
}

// Routers returns a snapshot of registered router names in registration order.
func (e *Engine) Routers() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]string, len(e.routerOrder))
	copy(out, e.routerOrder)
	return out
}

// Methods returns the wire method names registered on router, in sorted order.
func (e *Engine) Methods(router string) []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	methods, ok := e.routers[router]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(methods))
	for name := range methods {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// panicSig builds the structured "bad method signature" panic message
// used by buildEntry. Includes the offending signature, the accepted
// forms, and a one-line hint so the consumer doesn't have to guess.
func panicSig(typeName string, m reflect.Method, reason, hint string) {
	panic(fmt.Sprintf(`rpc.Engine.Register: %s.%s has unsupported signature — %s
  got:    %s
  expect: func (r *%s) %s(ctx *rpc.Context) error
          func (r *%s) %s(ctx *rpc.Context) (T, error)
          func (r *%s) %s(ctx *rpc.Context, p *Params) error
          func (r *%s) %s(ctx *rpc.Context, p *Params) (T, error)
  hint:   %s`,
		typeName, m.Name, reason,
		m.Type.String(),
		typeName, m.Name,
		typeName, m.Name,
		typeName, m.Name,
		typeName, m.Name,
		hint))
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}
