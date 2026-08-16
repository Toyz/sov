package main

import "github.com/Toyz/sov/rpc"

type Tenant struct {
	ID   string `json:"id"`
	Plan string `json:"plan"`
}

type TenantRouter struct{}

// WhoamiIn binds entirely from request HEADERS, not the body — the explorer
// renders header inputs and sends them as HTTP headers on execute.
type WhoamiIn struct {
	Tenant    string `sov:"header=X-Tenant-Id,required"`
	RequestID string `sov:"header=X-Request-Id"`
}

func (TenantRouter) Whoami(_ *rpc.Context, in *WhoamiIn) (Tenant, error) {
	return Tenant{ID: in.Tenant, Plan: "pro"}, nil
}

type SetPlanIn struct {
	_     struct{} `sov:"perm=tenant:admin"` // perm requirement
	Plan  string   `sov:"plan,0,required,title=Plan,example=pro"`
	Seats int      `sov:"seats,1,title=Seats,example=10"`
}

// SetPlan uses positional params plus a perm requirement.
func (TenantRouter) SetPlan(_ *rpc.Context, in *SetPlanIn) (Tenant, error) {
	return Tenant{ID: "t_1", Plan: in.Plan}, nil
}
