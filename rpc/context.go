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
	// codec is the per-request body codec the transport adapter selected
	// via Content-Type negotiation (HELL-286). nil means "use the engine
	// default" — the JSON PEMM wire. Set through SelectCodec; read by the
	// engine on dispatch.
	codec Codec
}

// SelectCodec pins the codec the engine uses for this request's params and
// result. The transport adapter calls it after resolving Content-Type;
// handlers normally never touch it. A nil codec leaves the engine default.
func (c *Context) SelectCodec(codec Codec) {
	if c != nil {
		c.codec = codec
	}
}

// selectedCodec returns the per-request codec, or nil when none was pinned.
func (c *Context) selectedCodec() Codec {
	if c == nil {
		return nil
	}
	return c.codec
}

// NewContext returns a Context wrapping ctx. State is created lazily on the
// first Set — a request that never stashes anything pays no map allocation.
func NewContext(ctx context.Context) *Context {
	return &Context{Context: ctx}
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
