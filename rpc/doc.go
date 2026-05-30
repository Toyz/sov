// Package rpc is the transport-agnostic controller layer for Sov.
//
// A consumer declares router structs and methods, then hands them to an
// Engine. The engine reflects the method signatures, maps wire names to
// Go methods, and dispatches incoming requests by router + method name.
//
//	type WidgetRouter struct {
//	    Repo *widget.Repo
//	}
//
//	type CreateParams struct {
//	    Name string `json:"name"`
//	}
//
//	func (r *WidgetRouter) Create(ctx *rpc.Context, p *CreateParams) (*Widget, error) {
//	    if p.Name == "" {
//	        return nil, rpc.BadRequest("name required")
//	    }
//	    return r.Repo.Create(ctx, p.Name)
//	}
//
//	engine := rpc.NewEngine()
//	engine.Register(&WidgetRouter{Repo: repo})
//
// Wire shape (the only contract):
//
//	POST /rpc/{Router}/{method}
//	Body:     {"args": [paramsObject]}
//	Resp 200: {"data": <result>}
//	Resp ≥400:{"error": {"message": "...", "code": "BAD_REQUEST"}}
//
// The engine does not know about HTTP. Transport adapters in sov/rpc/httpx
// and sov/rpc/fiberx own the HTTP boundary; both call Engine.Dispatch.
// The gateway in sov/gateway wraps an Engine for in-process services
// (the modular-monolith case) AND a Resolver chain for remote services
// (the microservice case), simultaneously.
//
// Auth is NOT the framework's concern. Consumers wire their own
// middleware that fills Context.User before the engine dispatches.
// Per-request integrity signing lives in sov/rpc/signing as a middleware
// that runs BEFORE the engine; it does not produce identity claims.
package rpc
