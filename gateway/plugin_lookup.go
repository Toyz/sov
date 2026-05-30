package gateway

import "log/slog"

// Log returns the structured logger framework + builtins should use.
// Returns the first-registered Logger plugin, or a slog.Default()
// adapter when none registered. Always non-nil — safe to call before
// any plugin is wired.
//
//	gw.Log().Info("plugin loaded", "name", p.PluginName())
func (g *Gateway) Log() Logger {
	g.muPlugins.RLock()
	defer g.muPlugins.RUnlock()
	for _, e := range g.plugins {
		if e.logger != nil {
			return e.logger
		}
	}
	return defaultLogger{l: slog.Default()}
}

// defaultLogger adapts slog.Default() to the Logger interface so
// gw.Log() is non-nil before any Logger plugin is wired.
type defaultLogger struct{ l *slog.Logger }

func (d defaultLogger) Debug(msg string, args ...any) { d.l.Debug(msg, args...) }
func (d defaultLogger) Info(msg string, args ...any)  { d.l.Info(msg, args...) }
func (d defaultLogger) Warn(msg string, args ...any)  { d.l.Warn(msg, args...) }
func (d defaultLogger) Error(msg string, args ...any) { d.l.Error(msg, args...) }

// PluginByName returns the registered plugin whose PluginName equals
// name, or nil if no plugin matches. Type-assert at the call site for
// typed access — sov stores plugins as `any` because the sub-interface
// they satisfy is what actually matters for dispatch.
//
//	if rid, ok := gw.PluginByName("request-id").(*requestid.Plugin); ok {
//	    fmt.Println(rid.LastIssued())
//	}
//
// PluginByName is the foundation of cross-plugin coordination — paired
// with PluginDependency (Requires + After) it lets one plugin opt in
// to behavior another plugin owns, without hard-coding import paths.
func (g *Gateway) PluginByName(name string) any {
	g.muPlugins.RLock()
	defer g.muPlugins.RUnlock()
	for _, e := range g.plugins {
		if e.name == name {
			// e.any is the original value passed to Use(), so the
			// caller can type-assert against the concrete plugin type
			// regardless of which sub-interfaces it satisfies.
			return e.any
		}
	}
	return nil
}

// PluginNames returns the registered plugin names in registration
// order. Useful for diagnostics tools that don't want to walk
// IntrospectReport.Plugins.
func (g *Gateway) PluginNames() []string {
	g.muPlugins.RLock()
	defer g.muPlugins.RUnlock()
	out := make([]string, 0, len(g.plugins))
	for _, e := range g.plugins {
		out = append(out, e.name)
	}
	return out
}
