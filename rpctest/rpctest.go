// Package rpctest is the test-ergonomics helper for sov consumers.
//
// Goal: a sov method test should be a function call. No HTTP, no
// gateway, no signing setup, no auth middleware — just construct a
// Context and invoke the engine.
//
//	func TestWidgetCreate(t *testing.T) {
//	    eng := rpc.NewEngine()
//	    eng.Register(&WidgetRouter{Store: fakeStore})
//
//	    ctx := rpctest.NewCtx().WithUser("u_alice")
//	    status, body := eng.Dispatch(ctx, "Widget", "create", []byte(`{"args":[{"name":"foo"}]}`))
//	    require.Equal(t, 200, status)
//	    require.Contains(t, string(body), `"name":"foo"`)
//	}
package rpctest

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

// NewCtx returns a fresh *rpc.Context backed by context.Background.
// Chain with WithUser / WithClaims / WithState to populate.
func NewCtx() *rpc.Context { return rpc.NewContext(context.Background()) }

// WithUser sets ctx.User. Pass the user id string the way the gateway
// would after dispatching from Claims.
func WithUser(ctx *rpc.Context, uid string) *rpc.Context {
	ctx.User = uid
	return ctx
}

// WithClaims stamps the full gateway.Claims on ctx — equivalent to
// what gateway.dispatchLocal does in production. ctx.User is set to
// claims.Subject; ctx.Claims() returns claims.
func WithClaims(ctx *rpc.Context, claims *gateway.Claims) *rpc.Context {
	ctx.User = claims.Subject
	ctx.Set(rpc.ContextKeyClaims, claims)
	return ctx
}

// WithState sets a key/value on ctx.State (handler-side scratch).
func WithState(ctx *rpc.Context, key string, value any) *rpc.Context {
	ctx.Set(key, value)
	return ctx
}

// Builder is a fluent wrapper over the With* functions above — it reads
// better in long test setups. Each method delegates to the matching free
// function so there's a single implementation of each mutation.
type Builder struct{ ctx *rpc.Context }

// New returns a Builder over a fresh Context.
func New() *Builder { return &Builder{ctx: NewCtx()} }

// Ctx returns the underlying *rpc.Context.
func (b *Builder) Ctx() *rpc.Context { return b.ctx }

// WithUser sets ctx.User and returns the builder.
func (b *Builder) WithUser(uid string) *Builder {
	WithUser(b.ctx, uid)
	return b
}

// WithClaims stamps Claims and returns the builder.
func (b *Builder) WithClaims(c *gateway.Claims) *Builder {
	WithClaims(b.ctx, c)
	return b
}

// WithScopes is sugar for WithClaims with a scopes-only claim. Roles
// are no longer carried on Claims — they live in the AuthzService at
// decision time. Use WithClaims directly if you need finer control.
func (b *Builder) WithScopes(uid string, scopes ...string) *Builder {
	return b.WithClaims(&gateway.Claims{
		Subject: uid, Scopes: scopes, ExpiresAt: time.Now().Add(1 * time.Hour).UTC(),
	})
}

// WithState sets a ctx.State key and returns the builder.
func (b *Builder) WithState(key string, value any) *Builder {
	WithState(b.ctx, key, value)
	return b
}

// Call dispatches one method through the engine and returns
// (status, decoded `data` as []byte, error from any wire layer). Sugar
// over rpc.Engine.Dispatch + envelope unwrap for the success path.
//
// On error responses the returned data is the raw envelope body and
// err is a *rpc.Error reconstructed from it — so test assertions can
// switch on err.(*rpc.Error).Code.
func Call(eng *rpc.Engine, ctx *rpc.Context, router, method string, params any) (status int, data []byte, err error) {
	body, mErr := wrapArgs(params)
	if mErr != nil {
		return 0, nil, mErr
	}
	status, raw := eng.Dispatch(ctx, router, method, body)
	if status >= 400 {
		return status, raw, decodeError(raw)
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if uErr := json.Unmarshal(raw, &env); uErr != nil {
		return status, raw, uErr
	}
	return status, []byte(env.Data), nil
}

// CallInto is Call with automatic unmarshal into out.
func CallInto(eng *rpc.Engine, ctx *rpc.Context, router, method string, params any, out any) (int, error) {
	status, data, err := Call(eng, ctx, router, method, params)
	if err != nil {
		return status, err
	}
	if out == nil {
		return status, nil
	}
	return status, json.Unmarshal(data, out)
}

func wrapArgs(params any) ([]byte, error) {
	if params == nil {
		return []byte(`{"args":[]}`), nil
	}
	p, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	wrapped, err := json.Marshal(struct {
		Args []json.RawMessage `json:"args"`
	}{Args: []json.RawMessage{p}})
	return wrapped, err
}

func decodeError(body []byte) error {
	// Status 0 — the caller fills it from the HTTP status if needed.
	if e, ok := rpc.DecodeErrorBody(body, 0); ok {
		return e
	}
	return fmt.Errorf("rpctest: response is not a JSON error envelope: %s", body)
}
