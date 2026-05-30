package gateway_test

import (
	"reflect"
	"testing"

	. "github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

// pluginInterfaces is every gateway plugin sub-interface whose hook
// methods must be skipped by the engine's RPC-method reflection scan
// (rpc.reservedMarkerMethods). When you add a new hook interface to
// plugin.go, add it here too — the test below then enforces that its
// method names are reserved.
//
// reflect cannot enumerate a package's interfaces automatically, so this
// list is hand-maintained; the value is that it couples the reserved set
// to the REAL interface method names, catching the common "added a hook,
// forgot to reserve its method" drift.
var pluginInterfaces = []reflect.Type{
	reflect.TypeOf((*Plugin)(nil)).Elem(),
	reflect.TypeOf((*PluginDoc)(nil)).Elem(),
	reflect.TypeOf((*HeaderInjector)(nil)).Elem(),
	reflect.TypeOf((*HeaderParser)(nil)).Elem(),
	reflect.TypeOf((*AuthTranslator)(nil)).Elem(),
	reflect.TypeOf((*DispatchHook)(nil)).Elem(),
	reflect.TypeOf((*BootValidator)(nil)).Elem(),
	reflect.TypeOf((*LifecycleHook)(nil)).Elem(),
	reflect.TypeOf((*IntrospectContributor)(nil)).Elem(),
	reflect.TypeOf((*Middlewarer)(nil)).Elem(),
	reflect.TypeOf((*ConfigApplier)(nil)).Elem(),
	reflect.TypeOf((*RouteHandler)(nil)).Elem(),
	reflect.TypeOf((*MeshConflictPolicy)(nil)).Elem(),
	reflect.TypeOf((*UpstreamTrustPolicy)(nil)).Elem(),
	reflect.TypeOf((*SealVerifier)(nil)).Elem(),
	reflect.TypeOf((*HealthAggregator)(nil)).Elem(),
	reflect.TypeOf((*ContextContributor)(nil)).Elem(),
	reflect.TypeOf((*ResponseInterceptor)(nil)).Elem(),
	reflect.TypeOf((*RecoveryHandler)(nil)).Elem(),
	reflect.TypeOf((*PluginDependency)(nil)).Elem(),
	reflect.TypeOf((*CapabilityProvider)(nil)).Elem(),
	// Header-cluster + framework-shape interfaces whose methods also land
	// in the reserved set. Previously unguarded — a router-shaped plugin
	// that also implemented Resolver/Server would have mis-detected its
	// Resolve/ListenAndServe as RPC methods if those ever fell out of the
	// reserved map. HeaderClaimer/Logger methods are listed in
	// markerExceptions (deliberately NOT reserved).
	reflect.TypeOf((*HeaderClaimer)(nil)).Elem(),
	reflect.TypeOf((*Resolver)(nil)).Elem(),
	reflect.TypeOf((*Server)(nil)).Elem(),
	reflect.TypeOf((*Logger)(nil)).Elem(),
}

// markerExceptions are hook-interface methods deliberately NOT reserved:
// they belong to interfaces a router-shaped plugin won't realistically
// also satisfy, or return non-RPC shapes. Listing them keeps the test
// honest about what's intentional vs. an oversight.
var markerExceptions = map[string]bool{
	"ClaimedHeaders": true, // HeaderClaimer — not reserved; avoid if your plugin is also a router
	"Debug":          true, // Logger
	"Info":           true, // Logger
	"Warn":           true, // Logger
	"Error":          true, // Logger
}

func TestReservedMarkers_CoverPluginInterfaces(t *testing.T) {
	for _, iface := range pluginInterfaces {
		for i := 0; i < iface.NumMethod(); i++ {
			name := iface.Method(i).Name
			if markerExceptions[name] {
				continue
			}
			if !rpc.IsReservedMarker(name) {
				t.Errorf("plugin interface %s method %q is not in rpc.reservedMarkerMethods — a router-shaped plugin implementing it would have %q mis-detected as an RPC method. Add it to the reservedMarkerMethods map in rpc/engine.go.",
					iface.Name(), name, name)
			}
		}
	}
}
