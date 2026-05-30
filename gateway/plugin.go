// Sov plugin system — sov-itself-PEMM.
//
// Extension is interface-driven. One entry point (`gw.Use(plugin)`),
// many optional sub-interfaces. Implement only the hooks you need;
// the gateway auto-detects each via Go interface assertion. Plugins
// can ALSO be sov services — if the plugin Go type has RPC methods,
// gw.Use registers them on the engine in the same call.
//
// The Plugin marker itself is empty by intent — a plugin is whatever
// satisfies AT LEAST ONE of the sub-interfaces below (or simply has
// RPC methods, in which case it's just a router). PluginName is
// required for diagnostics + the explorer's plugin tab.

package gateway
