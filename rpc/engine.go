package rpc

import (
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
)

// Logger is the minimal structured-log sink the engine writes non-fatal
// boot warnings to (e.g. an exported RPC-shaped method dropped because its
// name collides with a reserved framework marker — HELL-281). Its Warn
// shape matches sov's gateway.Logger, so the gateway injects its own logger
// via SetLogger and engine warnings share the app's structured logger
// instead of the stdlib log package. When none is injected the engine falls
// back to slog.Default(). Not a hot path — called only at Register.
type Logger interface {
	Warn(msg string, args ...any)
}

// Engine holds the registered routers and dispatches incoming requests
// to the right Go method by reflection. Safe for concurrent dispatch.
// Mutations (Register) are expected at boot, not under load.
type Engine struct {
	mu             sync.RWMutex
	routers        map[string]map[string]*methodEntry
	publicList     map[string][]string         // router → wire method names declared via PublicMethods()
	hiddenList     map[string][]string         // router → SOFT-hidden wire names declared via HiddenMethods()
	hardHiddenList map[string][]string         // router → HARD-hidden wire names declared via HardHiddenMethods()
	authorizers    map[string]MethodAuthorizer // router → in-process resource-scoped authz hook (HELL-283)
	routerOrder    []string
	// codecs is the per-name registry of BUSINESS-body codecs (HELL-286),
	// keyed by Codec.Name(); a request selects one by Content-Type.
	// defaultCodec is used when a request selects none — the JSON PEMM wire.
	// Read on the dispatch hot path; mutated only at boot.
	codecs       map[string]Codec
	defaultCodec Codec
	// negotiable is true once more than one codec is registered — the only
	// case where per-request Content-Type negotiation can change the codec.
	// The transport adapter checks it to skip negotiation entirely in the
	// common single-codec (JSON-only) deployment. An atomic so the dispatch
	// hot path reads it without a lock.
	negotiable atomic.Bool
	// logger sinks non-fatal boot warnings; nil falls back to slog.Default().
	logger Logger
}

// SetLogger injects the structured logger the engine writes boot warnings
// to (HELL-281). The gateway wires its own Logger here so engine warnings
// share the app's logger; call at boot, before Register.
func (e *Engine) SetLogger(l Logger) {
	e.mu.Lock()
	e.logger = l
	e.mu.Unlock()
}

// warn emits a boot warning through the injected logger, or slog.Default()
// when none is set. Structured key/value args, never a fmt string.
func (e *Engine) warn(msg string, args ...any) {
	e.mu.RLock()
	l := e.logger
	e.mu.RUnlock()
	if l != nil {
		l.Warn(msg, args...)
		return
	}
	slog.Default().Warn(msg, args...)
}

// NewEngine returns an empty Engine with the JSON codec registered as the
// default.
func NewEngine() *Engine {
	return &Engine{
		routers:        map[string]map[string]*methodEntry{},
		publicList:     map[string][]string{},
		hiddenList:     map[string][]string{},
		hardHiddenList: map[string][]string{},
		authorizers:    map[string]MethodAuthorizer{},
		codecs:         map[string]Codec{jsonName: jsonCodec{}},
		defaultCodec:   jsonCodec{},
	}
}

// RegisterCodec adds a codec to the registry under its Name() so a request
// can select it (e.g. by Content-Type). Call at boot. It does NOT change
// the default — use SetCodec for that. The JSON default remains the
// cross-language PEMM wire; a non-JSON codec is a per-deployment choice a
// caller opts into per request.
func (e *Engine) RegisterCodec(c Codec) {
	if c == nil {
		return
	}
	e.mu.Lock()
	e.codecs[c.Name()] = c
	if len(e.codecs) > 1 {
		e.negotiable.Store(true)
	}
	e.mu.Unlock()
}

// Negotiable reports whether more than one codec is registered — i.e.
// whether per-request Content-Type negotiation can actually change the
// codec. False in the common JSON-only deployment, letting the transport
// adapter skip the Content-Type parse on the hot path.
func (e *Engine) Negotiable() bool { return e.negotiable.Load() }

// SetCodec sets the DEFAULT codec (and registers it). Back-compat with the
// single-codec API — a request that selects no codec uses this one. Passing
// nil restores the JSON default.
func (e *Engine) SetCodec(c Codec) {
	if c == nil {
		c = jsonCodec{}
	}
	e.mu.Lock()
	e.codecs[c.Name()] = c
	e.defaultCodec = c
	if len(e.codecs) > 1 {
		e.negotiable.Store(true)
	}
	e.mu.Unlock()
}

// ResolveCodec returns the registered codec named name, or the default when
// name is unknown or empty. This is the negotiation lookup the transport
// adapter calls after mapping Content-Type to a codec name.
func (e *Engine) ResolveCodec(name string) Codec {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if c, ok := e.codecs[name]; ok {
		return c
	}
	if e.defaultCodec != nil {
		return e.defaultCodec
	}
	return jsonCodec{}
}

// activeCodec returns the engine's default codec, used when a request
// carries no per-request selection.
func (e *Engine) activeCodec() Codec {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.defaultCodec == nil {
		return jsonCodec{}
	}
	return e.defaultCodec
}

// codecForContext prefers a per-request codec the adapter selected onto ctx
// (Content-Type negotiation), falling back to the engine default.
func (e *Engine) codecForContext(ctx *Context) Codec {
	if ctx != nil {
		if c := ctx.selectedCodec(); c != nil {
			return c
		}
	}
	return e.activeCodec()
}

// encodeErrorWith renders rerr through codec, falling back to JSON if the
// codec itself fails to encode (so an error is never swallowed).
func encodeErrorWith(codec Codec, rerr *Error) (int, []byte) {
	body, err := codec.EncodeError(rerr)
	if err != nil {
		return rerr.Status, MarshalError(rerr)
	}
	return rerr.Status, body
}

// asRPCError extracts an *Error from err, or returns fallback when err is
// not (and does not wrap) one.
func asRPCError(err error, fallback *Error) *Error {
	var r *Error
	if errors.As(err, &r) {
		return r
	}
	return fallback
}

// PublicLister is the optional marker a router implements to publish a
// DISCOVERY hint: which methods the author considers public. The engine
// reads the list once at Register, exposes it via PublicMethods(router)
// and Describe(), and skips the marker method itself when reflecting the
// RPC surface.
//
// SECURITY (HELL-279): this list is introspection/discovery ONLY. It does
// NOT gate access — the gateway's authz middleware never consults it, so
// marking a method here does NOT make it callable without auth. The
// AuthzService is the sole access boundary. To make a method genuinely
// anonymous-callable, DECLARE its requirement — e.g. `sov:"perm=public"`
// or AuthzRequirements()["foo"]="public" (HELL-280) — and have your
// AuthzService allow the "public" token. sov keeps that token opaque; the
// meaning of "public" is the AuthzService's to honor.
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
// Names may be given in wire (lowerFirst) OR Go casing — the engine
// normalizes them (HELL-284); a name matching no method panics at Register
// rather than silently hiding nothing.
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
// Names may be given in wire (lowerFirst) OR Go casing — the engine
// normalizes them (HELL-284); a name matching no method panics at Register.
//
// SECURITY: hard-hide removes discoverability, NOT access. The endpoint is
// still live and callable; authz, not hiding, is the access boundary.
type HardHiddenLister interface {
	HardHiddenMethods() []string
}

// AuthzRequirer is the optional marker a router implements to declare
// per-method authz requirements in bulk, keyed by WIRE method name
// (create, roleUpsert — lowerFirst of the Go name). The returned tokens
// are OPAQUE: the engine reflects them onto each method and carries them
// into CheckParams.Perm, but never interprets them — the consumer's
// AuthzService decides what "pages:write" or "public" grants.
//
// Precedence: a `sov:"perm=…"` sentinel on the method's params struct
// WINS; AuthzRequirements only fills methods the tag left undeclared.
// This makes the marker the natural home for bulk defaults and for
// no-param methods (which have no params struct to tag), while an
// inline tag can still override one method. Naming a method that is not
// registered on the router is a boot panic (typo guard).
type AuthzRequirer interface {
	AuthzRequirements() map[string]string
}

// MethodAuthorizer is the optional marker a router implements to authorize
// calls to its OWN methods IN-PROCESS — after params are decoded and before
// the handler runs. This is the resource-scoped authz tier the central
// AuthzService cannot reach: Check runs out-of-band with no decoded args
// and no access to the router's store, so it can gate "may this caller call
// note:write at all" (claims + perm + headers) but not "may they write THIS
// node, which lives in a space only the DB knows" (HELL-283).
//
// The engine calls AuthorizeMethod for every reflected method on the router
// (wire name in `method`), passing the decoded params pointer (nil for
// no-param methods). Return nil to allow; return an *rpc.Error
// (Forbidden/Unauthorized) to deny — dispatch surfaces it verbatim and the
// handler never runs. It runs on EVERY dispatch, in-process or via the
// gateway, so a router self-guards even in a monolith with no gateway authz.
//
// CONFUSED-DEPUTY DISCIPLINE (read this): authorize the EXACT target the
// handler will act on. If AuthorizeMethod checks params.WorkspaceID but the
// handler resolves its target from a different field or a store-derived
// space, the authorized target ≠ the acted target = a bypass. Nothing in
// the framework enforces the match — keep the target-resolution code shared
// between this hook and the handler. When the target is DERIVED from the
// store (a node's space_id), do the check where you load the node — inside
// the handler — not here, because this hook has params but not yet the row.
//
// SCOPE: honored for Register (reflection) routers. Methods added under a
// router name via rpc.Handle are typed closures with no router struct to
// carry this interface — those self-authorize inside their fn.
type MethodAuthorizer interface {
	AuthorizeMethod(ctx *Context, method string, params any) error
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
	"AuthorizeMethod":      true,
	"AuthzRequirements":    true,
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
	// perm is the declarative authz requirement for this method (HELL-280):
	// an OPAQUE token the consumer's AuthzService interprets — the engine
	// never parses it. The same discipline as Claims keeping RBAC out
	// (claims.go): the moment the framework understands what "pages:write"
	// means it stops being generic. Sourced from a `sov:"perm=…"` blank-`_`
	// sentinel on the params struct (tag wins) AND/OR the router's
	// AuthzRequirements() marker (fills gaps); empty when undeclared, and an
	// empty perm leaves the default to the consumer's Check. Carried to
	// Describe and into CheckParams.Perm so the requirement rides next to
	// the handler instead of a parallel service→requirement map.
	perm string
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
	// Wire name = the type name with a trailing "Router" stripped when present
	// (NoteToolsRouter -> NoteTools); otherwise the type name verbatim
	// (Notes -> Notes). "Router" is a convention for a clean wire name, no
	// longer a hard requirement — a struct registers under whatever it is named,
	// and surfaces discover it by capability (see Engine.Find), not by name.
	routerName := strings.TrimSuffix(typeName, "Router")

	methods := map[string]*methodEntry{}
	for i := 0; i < rt.NumMethod(); i++ {
		m := rt.Method(i)
		if !m.IsExported() {
			continue
		}
		if reservedMarkerMethods[m.Name] {
			if looksLikeStrayRPCMethod(m) {
				e.warn("rpc.Register: exported method is shaped like an RPC handler but its name is a reserved framework marker; it is NOT exposed over RPC (calls 404) — rename it to expose the method",
					"router", typeName, "method", m.Name)
			}
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

	// Marker lists name methods by WIRE name (lowerFirst of the Go name).
	// A consumer who returns the Go name ("Secret" for wire "secret") would
	// otherwise silently fail to hide/expose the method — a security-shaped
	// no-op for the hide markers. normalizeMarkerNames accepts either casing
	// and panics on a name that matches NO method, so a typo fails loud at
	// boot instead of silently leaving a method exposed.
	var public, hidden, hardHidden []string
	if lister, ok := router.(PublicLister); ok {
		public = normalizeMarkerNames(typeName, "PublicMethods", methods, lister.PublicMethods())
	}
	if lister, ok := router.(HiddenLister); ok {
		hidden = normalizeMarkerNames(typeName, "HiddenMethods", methods, lister.HiddenMethods())
	}
	if lister, ok := router.(HardHiddenLister); ok {
		hardHidden = normalizeMarkerNames(typeName, "HardHiddenMethods", methods, lister.HardHiddenMethods())
	}

	// Overlay bulk authz requirements from the AuthzRequirer marker. Tag
	// wins: a sov:"perm=…" sentinel already set entry.perm, so only fill
	// methods the tag left undeclared. Keys accept wire OR Go casing;
	// naming an unknown method is a typo and panics (fail fast at boot,
	// never a silent no-op).
	if reqr, ok := router.(AuthzRequirer); ok {
		for name, perm := range reqr.AuthzRequirements() {
			ent := methods[name]
			if ent == nil {
				ent = methods[lowerFirst(name)]
			}
			if ent == nil {
				panic(fmt.Sprintf("rpc.Engine.Register: %s.AuthzRequirements names unknown method %q (no wire method %q or %q)", typeName, name, name, lowerFirst(name)))
			}
			if ent.perm == "" {
				ent.perm = perm
			}
		}
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
	if ma, ok := router.(MethodAuthorizer); ok {
		e.authorizers[routerName] = ma
	}
	e.routerOrder = append(e.routerOrder, routerName)
}

// authorizerFor returns the router's in-process MethodAuthorizer hook, or
// nil if it declared none.
func (e *Engine) authorizerFor(router string) MethodAuthorizer {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.authorizers[router]
}

// PublicMethods returns the wire method names the router published via
// the PublicLister marker, or nil. DISCOVERY hint only (HELL-279): this
// is consumed by introspection/Describe, never by access control. It does
// not skip authz — the AuthzService gates every call. To express "callable
// anonymously", declare a perm token (HELL-280) your AuthzService allows.
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

// Perm returns the declarative authz requirement token declared for
// router/method (HELL-280), or "" when the method is unknown or declared
// none. OPAQUE — the gateway hands it to the AuthzService via
// CheckParams.Perm and sov never interprets it. An empty return leaves the
// default to the consumer's Check.
func (e *Engine) Perm(router, method string) string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	methods, ok := e.routers[router]
	if !ok {
		return ""
	}
	ent, ok := methods[method]
	if !ok {
		return ""
	}
	return ent.perm
}

// normalizeMarkerNames resolves each marker-list entry to the WIRE method
// name, accepting either the wire name or the Go method name (lowerFirst).
// It panics if an entry matches no method — a hide/expose marker that
// silently no-ops is worse than a boot failure (you believe a method is
// hidden when it is not). Returns nil for an empty input.
func normalizeMarkerNames(typeName, marker string, methods map[string]*methodEntry, names []string) []string {
	if len(names) == 0 {
		return nil
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		switch {
		case methods[n] != nil:
			out = append(out, n)
		case methods[lowerFirst(n)] != nil:
			out = append(out, lowerFirst(n))
		default:
			panic(fmt.Sprintf("rpc.Engine.Register: %s.%s names unknown method %q (no wire method %q or %q); markers use wire names — the lowerFirst of the Go method name",
				typeName, marker, n, n, lowerFirst(n)))
		}
	}
	return out
}

func copySlice(src []string) []string {
	if len(src) == 0 {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// looksLikeStrayRPCMethod reports whether m — already known to collide
// with a reserved framework marker — is shaped like an RPC method the
// author probably meant to expose over the wire, as opposed to a genuine
// plugin hook that happens to share the name. Used only to decide whether
// to warn at Register (HELL-281); never affects dispatch.
//
// The RPC shape is (*rpc.Context[, *Params]) (…, error). The one reserved
// marker that also takes *rpc.Context is ContributeContext, whose second
// arg is the gateway *Request — carve that out by rejecting a second
// parameter whose struct type is named "Request". Every other plugin hook
// takes context.Context / *Request / etc. as its FIRST arg and is already
// excluded by the In(1) == *rpc.Context check.
func looksLikeStrayRPCMethod(m reflect.Method) bool {
	mt := m.Type
	if mt.NumIn() < 2 || mt.NumIn() > 3 {
		return false
	}
	if mt.In(1) != ctxType {
		return false
	}
	if mt.NumIn() == 3 {
		p := mt.In(2)
		if p.Kind() == reflect.Ptr && p.Elem().Kind() == reflect.Struct && p.Elem().Name() == "Request" {
			return false
		}
	}
	numOut := mt.NumOut()
	if numOut < 1 || numOut > 2 {
		return false
	}
	return mt.Out(numOut - 1).Implements(errType)
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
		if err := RejectNestedHeaders(entry.paramType); err != nil {
			panic(fmt.Sprintf("rpc.Engine.Register: %s.%s params %s: %v", typeName, m.Name, entry.paramType, err))
		}
		entry.fieldMap = fm
		entry.internal = fm.Internal
		entry.internalHard = fm.InternalHard
		entry.perm = fm.Perm
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

// suggestMethod returns the registered wire method name a caller most
// likely meant when method missed, or "" when there is no near match.
// The common miss is Go casing on the wire (List vs the wire name list):
// try lowerFirst first, then a case-insensitive match. Discovery hint
// only — never used for dispatch. See HELL-282.
func (e *Engine) suggestMethod(router, method string) string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	methods, ok := e.routers[router]
	if !ok {
		return ""
	}
	if lf := lowerFirst(method); lf != method {
		if _, ok := methods[lf]; ok {
			return lf
		}
	}
	for wire := range methods {
		if strings.EqualFold(wire, method) {
			return wire
		}
	}
	return ""
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
