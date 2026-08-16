package main

import "github.com/Toyz/sov/rpc"

// Money is a shared value type: both Catalog.Price and Billing.Charge return it.
// Same name, SAME shape everywhere — a legitimately shared model (multi-owner
// info), NOT drift.
type Money struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// Item is the Catalog entity — Catalog owns it (returns it); other routers only
// reference it.
type Item struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Price Money    `json:"price"` // nested named type
	Tags  []string `json:"tags"`
}

type Event struct {
	Kind   string `json:"kind"`
	ItemID string `json:"item_id"`
	At     string `json:"at"`
}

type Ack struct {
	OK bool `json:"ok"`
}

type CatalogRouter struct{}

type CreateItemIn struct {
	Name  string   `sov:"name,0,required,maxlen=80,title=Name,desc=Human label for the item,example=Widget"`
	Price Money    `sov:"price,1,title=Price,desc=Unit price"`
	Tags  []string `sov:"tags,2,title=Tags,desc=Free-form labels"`
}

func (CatalogRouter) Create(_ *rpc.Context, in *CreateItemIn) (Item, error) {
	return Item{ID: "itm_1", Name: in.Name, Price: in.Price, Tags: in.Tags}, nil
}

type ItemID struct {
	ID string `sov:"id,0,required,title=Item id,example=itm_1"`
}

func (CatalogRouter) Get(_ *rpc.Context, in *ItemID) (Item, error) {
	return Item{ID: in.ID, Name: "Widget", Price: Money{Amount: 999, Currency: "USD"}, Tags: []string{"new"}}, nil
}

// List is paginated: takes rpc.PageParams, returns rpc.Page[Item].
func (CatalogRouter) List(_ *rpc.Context, _ *rpc.PageParams) (rpc.Page[Item], error) {
	return rpc.Page[Item]{
		Items:   []Item{{ID: "itm_1", Name: "Widget", Price: Money{Amount: 999, Currency: "USD"}}},
		HasMore: false,
	}, nil
}

// Price returns the shared Money type (also returned by Billing.Charge).
func (CatalogRouter) Price(_ *rpc.Context, _ *ItemID) (Money, error) {
	return Money{Amount: 999, Currency: "USD"}, nil
}

// Stats takes no params — renders as a "no args" method.
func (CatalogRouter) Stats(_ *rpc.Context) (map[string]int, error) {
	return map[string]int{"items": 42}, nil
}

type WatchIn struct {
	Kinds []string `sov:"kinds,0,title=Kinds,desc=Event kinds to include (empty = all)"`
}

// Watch server-streams events (rpc.Stream[Event] -> NDJSON).
func (CatalogRouter) Watch(_ *rpc.Context, _ *WatchIn) (rpc.Stream[Event], error) {
	return rpc.StreamSlice([]Event{
		{Kind: "created", ItemID: "itm_1", At: "t0"},
		{Kind: "updated", ItemID: "itm_1", At: "t1"},
	}), nil
}

type DeleteIn struct {
	_  struct{} `sov:"perm=catalog:write"` // method authz requirement (opaque)
	ID string   `sov:"id,0,required,title=Item id,example=itm_1"`
}

func (CatalogRouter) Delete(_ *rpc.Context, _ *DeleteIn) (Ack, error) {
	return Ack{OK: true}, nil
}

type ImportIn struct {
	_   struct{} `sov:"deprecated=use Create instead"` // deprecated METHOD
	Raw string   `sov:"raw,0,required,maxlen=4096,title=Raw payload"`
}

func (CatalogRouter) Import(_ *rpc.Context, _ *ImportIn) (Ack, error) {
	return Ack{OK: true}, nil
}

type DumpIn struct {
	_ struct{} `sov:"internal"` // soft-internal: hidden unless the explorer "internal" toggle is on
}

func (CatalogRouter) Debugdump(_ *rpc.Context, _ *DumpIn) (map[string]any, error) {
	return map[string]any{"ok": true}, nil
}
