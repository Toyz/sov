// Package gateway — hook panic safety + structured error reporting.
//
// Every plugin hook invocation flows through safeHook which:
//   1. Defers recover() so a panic in a plugin cannot crash the
//      gateway.
//   2. Captures returned errors uniformly with panics.
//   3. Dispatches the failure to registered RecoveryHandler plugins
//      (with a default Logger-backed fallback when none are
//      registered).
//   4. Returns the plugin's chosen reaction via the returned error:
//         nil               — success
//         HaltErr(err)      — boot path bubbles up, refuses startup
//         RespondErr(r, e)  — request path returns r to the caller
//         any other err     — logged + continue (soft)
//      Panics are treated as soft for request hooks and halt for boot
//      hooks (the bootHooks set encodes the position).

package gateway

import (
	"fmt"
	"runtime/debug"
)

// defaultRecoveryHandler is the framework's fallback when no
// RecoveryHandler plugin is registered. Logs structured failure info
// via gw.Log(); never overrides the response.
type defaultRecoveryHandler struct{ gw *Gateway }

func (d *defaultRecoveryHandler) HandleHookFailure(f HookFailure) *Response {
	if d.gw == nil {
		return nil
	}
	logger := d.gw.Log()
	pname := f.PluginName
	if pname == "" {
		pname = "<anonymous>"
	}
	attrs := []any{
		"hook", f.HookName,
		"plugin", pname,
	}
	if f.Panic != nil {
		attrs = append(attrs, "panic", fmt.Sprint(f.Panic), "stack", string(f.Stack))
		logger.Error("sov hook panic", attrs...)
	} else {
		attrs = append(attrs, "err", f.Err.Error())
		logger.Error("sov hook error", attrs...)
	}
	return nil
}

// bootHooks lists the hook names where a panic should be treated as
// fatal (halt boot). Request-path hooks get soft treatment.
var bootHooks = map[string]bool{
	"BootValidator":         true,
	"ConfigApplier":         true,
	"LifecycleHook.OnStart": true,
}

// safeHook wraps a single plugin-hook invocation. Returns:
//   - override: a Response from a registered RecoveryHandler OR
//     from a RespondErr return value. nil when no override.
//   - bootErr: non-nil HaltErr when the hook signals halt (either
//     by returning HaltErr or by panicking from a boot-position
//     hook).
//   - failed: true when the hook errored or panicked.
//
// fn returns the hook's error (or nil). Panics are recovered.
func (g *Gateway) safeHook(hookName, pluginName string, fn func() error) (override *Response, bootErr error, failed bool) {
	var caughtPanic any
	var stack []byte
	var hookErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				caughtPanic = r
				stack = debug.Stack()
			}
		}()
		hookErr = fn()
	}()
	if caughtPanic == nil && hookErr == nil {
		return nil, nil, false
	}
	failure := HookFailure{
		HookName:   hookName,
		PluginName: pluginName,
		Err:        hookErr,
		Panic:      caughtPanic,
		Stack:      stack,
	}
	if caughtPanic != nil {
		failure.Err = fmt.Errorf("panic: %v", caughtPanic)
		// Boot-position panics escalate to halt automatically.
		if bootHooks[hookName] {
			failure.Err = HaltErr(failure.Err)
		}
	}
	recoveryOverride := g.dispatchRecovery(failure)
	// Plugin-supplied RespondErr always wins; recovery handler
	// override is the fallback.
	if r := ResponseFrom(failure.Err); r != nil {
		override = r
	} else if recoveryOverride != nil {
		override = recoveryOverride
	}
	if IsHalt(failure.Err) {
		bootErr = failure.Err
	}
	return override, bootErr, true
}

// dispatchRecovery calls registered RecoveryHandler plugins in
// order; first non-nil override wins. Falls back to the default
// Logger-backed handler when no plugin handlers are registered.
func (g *Gateway) dispatchRecovery(f HookFailure) *Response {
	snap := g.snapshotPlugins()
	var override *Response
	found := false
	for _, e := range snap {
		if e.recoveryHandler == nil {
			continue
		}
		found = true
		if r := e.recoveryHandler.HandleHookFailure(f); r != nil && override == nil {
			override = r
		}
	}
	if !found {
		return g.defaultRecovery.HandleHookFailure(f)
	}
	return override
}
