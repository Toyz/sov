// Package shipping defines an Address shape that DIVERGES from
// accounts.Address — same type name, different fields — to demonstrate drift.
package shipping

import "github.com/Toyz/sov/rpc"

type Address struct {
	Line1   string `json:"line1"`
	Line2   string `json:"line2"`
	Country string `json:"country"`
}

type Shipment struct {
	ID string  `json:"id"`
	To Address `json:"to"`
}

type ShippingRouter struct{}

type TrackIn struct {
	ID string `sov:"id,0,required,title=Shipment id,example=shp_1"`
}

func (ShippingRouter) Track(_ *rpc.Context, in *TrackIn) (Shipment, error) {
	return Shipment{ID: in.ID, To: Address{Line1: "12 Elm Ave", Line2: "Unit 3", Country: "US"}}, nil
}
