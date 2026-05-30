package rpc

import "context"

// Context is the per-request value handed to every router method. It
// embeds the standard library context.Context so it can be passed wherever
// a context.Context is expected.
//
// User is the authenticated subject id (an opaque string the gateway
// resolved from the bearer token). The framework does not produce it —
// consumers wire whatever auth middleware they need (JWT, session
// cookie, upstream gateway headers) before dispatch and set it here.
// Handlers that need the subject call rpc.RequireSubject(ctx); handlers
// that want the full structured Claims call ctx.Claims().
//
// State is a free-form bag for adapter- and consumer-specific values
// (database handles, fiber.Ctx, request id, etc.). The framework does not
// read it; it is provided so consumers do not need to subclass Context
// or thread their own context type through every handler.
type Context struct {
	context.Context
	User  any
	State map[string]any
}

// NewContext returns a Context wrapping ctx.
func NewContext(ctx context.Context) *Context {
	return &Context{Context: ctx, State: map[string]any{}}
}

// Set stashes a value in State under key, creating the map if needed.
func (c *Context) Set(key string, v any) {
	if c.State == nil {
		c.State = map[string]any{}
	}
	c.State[key] = v
}

// Get returns the value at key, or nil.
func (c *Context) Get(key string) any {
	if c.State == nil {
		return nil
	}
	return c.State[key]
}

// UserFromContext returns the authenticated user, or an Unauthorized
// Error if Context.User is nil. Routers reach for this rather than
// type-asserting Context.User directly so the error path is consistent.
func UserFromContext(c *Context) (any, error) {
	if c == nil || c.User == nil {
		return nil, Unauthorized("authentication required")
	}
	return c.User, nil
}
