package main

import "github.com/Toyz/sov/rpc"

type Charge struct {
	ID     string `json:"id"`
	Amount Money  `json:"amount"` // shared Money type
	Note   string `json:"note"`
}

type BillingRouter struct{}

type ChargeIn struct {
	Amount   Money  `sov:"amount,0,required,title=Amount"`
	Note     string `sov:"note,1,title=Note,maxlen=140"`
	LegacyID string `sov:"legacy_id,2,deprecated,title=Legacy id,desc=Old identifier — do not use"` // deprecated FIELD
}

// Charge returns the shared Money type — same shape Catalog.Price returns, so
// Money has two owners but is NOT drift.
func (BillingRouter) Charge(_ *rpc.Context, in *ChargeIn) (Money, error) {
	return in.Amount, nil
}

// History is paginated.
func (BillingRouter) History(_ *rpc.Context, _ *rpc.PageParams) (rpc.Page[Charge], error) {
	return rpc.Page[Charge]{
		Items: []Charge{{ID: "chg_1", Amount: Money{Amount: 500, Currency: "USD"}, Note: "demo"}},
	}, nil
}
