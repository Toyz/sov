package rpc

// Page is the canonical pagination envelope: a slice of items plus an opaque
// cursor to fetch the next page. Return *Page[T] from a list method and pair the
// params with PageParams so every generated client gets a uniform paging shape
// instead of each service inventing its own.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

// PageParams is the request mixin for a paginated method: a page size and an
// opaque cursor echoed from the previous Page.NextCursor. Embed it in a params
// struct.
type PageParams struct {
	Limit  int    `json:"limit,omitempty" sov:"limit,title=Page size"`
	Cursor string `json:"cursor,omitempty" sov:"cursor,title=Opaque next-page cursor"`
}
