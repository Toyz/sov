package rpc_test

import (
	"testing"

	"github.com/Toyz/sov/rpc"
)

type Item struct {
	ID string `json:"id"`
}
type ListParams struct {
	rpc.PageParams
	Q string `json:"q"`
}
type ListRouter struct{}

func (ListRouter) List(_ *rpc.Context, _ *ListParams) (*rpc.Page[Item], error) {
	return &rpc.Page[Item]{Items: []Item{{ID: "1"}}, HasMore: false}, nil
}

// The reflection descriptor must handle a method returning a generic Page[T]
// without choking — the whole point of a reusable pagination type.
func TestPage_DescribesWithoutError(t *testing.T) {
	eng := rpc.NewEngine()
	eng.Register(&ListRouter{})
	rds := eng.Describe()
	if len(rds) == 0 {
		t.Fatal("no routers described")
	}
	// Just reaching here (Register + Describe didn't panic/error) proves the
	// generic return type is handled.
}
