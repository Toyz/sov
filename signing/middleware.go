package signing

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

// GatewayMiddleware is a gateway.Middleware that gates every business RPC
// with the Validator. Framework endpoints (/rpc/_*) bypass the check —
// the gateway owns those, and session/init RPCs need to land somewhere
// signature-free or new clients cannot get a session in the first place.
//
// SkipMethods is the consumer-supplied list of "{Service}/{method}"
// pairs that bypass validation. Always include the session-init pair
// the consumer defines (e.g. "Session/init"). If empty, every business
// RPC requires a valid signature.
//
// OnSuccess, if non-nil, is invoked with the verified sessionID after a
// successful validation. Typical use: stash the sessionID on the
// request so downstream handlers can resolve "who is this session's
// user?" without re-doing the lookup.
type MiddlewareOptions struct {
	Validator   *Validator
	SkipMethods []string
	OnSuccess   func(req *gateway.Request, sessionID string)
}

// GatewayMiddleware returns the gateway.Middleware described above.
func GatewayMiddleware(opts MiddlewareOptions) gateway.Middleware {
	skip := map[string]struct{}{}
	for _, s := range opts.SkipMethods {
		skip[s] = struct{}{}
	}
	return func(next gateway.Handler) gateway.Handler {
		return func(ctx context.Context, req *gateway.Request) *gateway.Response {
			// Framework endpoints (/rpc/_foo) always bypass.
			if strings.HasPrefix(req.Path, "/rpc/_") {
				return next(ctx, req)
			}
			router, method, ok := rpc.SplitRPCPath(req.Path)
			if !ok {
				return next(ctx, req) // malformed paths: let the gateway produce the standard 404
			}
			if _, exempt := skip[router+"/"+method]; exempt {
				return next(ctx, req)
			}

			sid, err := opts.Validator.Validate(ctx, gwHeaders(req.Header), router, method, req.Body)
			if err != nil {
				return failureResponse(err)
			}
			if opts.OnSuccess != nil {
				opts.OnSuccess(req, sid)
			}
			return next(ctx, req)
		}
	}
}

// gwHeaders adapts gateway.Header to Headers (the validator's tiny interface).
type gwHeaders gateway.Header

func (g gwHeaders) Get(k string) string { return gateway.Header(g).Get(k) }

func failureResponse(err error) *gateway.Response {
	fail, ok := err.(*Failure)
	if !ok {
		fail = &Failure{Reason: ReasonSessionLookup, Message: err.Error()}
	}
	status := 403
	if fail.Reason == ReasonSessionLookup {
		status = 503
	}
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": fail.Message,
			"code":    string(fail.Reason),
		},
	})
	return &gateway.Response{Status: status, Body: body}
}
