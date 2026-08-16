// Package accounts defines an Address shape that DIVERGES from
// shipping.Address — same type name, different fields. Registering both routers
// makes "Address" show up in the explorer's drift radar (cross_refs).
package accounts

import "github.com/Toyz/sov/rpc"

type Address struct {
	Street string `json:"street"`
	City   string `json:"city"`
	Zip    string `json:"zip"`
}

type Account struct {
	ID      string  `json:"id"`
	Billing Address `json:"billing"`
}

type AccountsRouter struct{}

type GetIn struct {
	ID string `sov:"id,0,required,title=Account id,example=acct_1"`
}

func (AccountsRouter) Get(_ *rpc.Context, in *GetIn) (Account, error) {
	return Account{ID: in.ID, Billing: Address{Street: "1 Main St", City: "Denver", Zip: "80014"}}, nil
}
