package rpc

import (
	"iter"
	"reflect"
)

// StreamProducer is the type-erased view of a server stream that a transport
// drains. The engine hands one to the gateway, which encodes each yielded value
// as one line of newline-delimited JSON (NDJSON). Returning false from yield
// stops iteration early — e.g. the client disconnected — so a handler streaming
// an unbounded source stops producing. Not part of the public authoring API;
// author with Stream[T].
type StreamProducer interface {
	ForEach(yield func(v any) bool)
}

// Stream[T] is the result type of a server-STREAMING method:
//
//	func (r *FeedRouter) Tail(rc *rpc.Context, p *TailParams) (rpc.Stream[Event], error)
//
// The handler validates and authorizes up front, then returns EITHER an error
// (a normal buffered error response — nothing has streamed yet) OR a Stream
// whose items the gateway writes as NDJSON, one JSON object per line, flushed as
// produced. The sequence is pulled lazily, so an unbounded or backpressured
// source streams without being buffered.
//
// There is deliberately no mid-stream error channel: once the Stream is
// returned the status line is already 200 and committed, so validate before
// returning it. Bidirectional streaming is an explicit non-goal.
type Stream[T any] struct {
	seq iter.Seq[T]
}

// StreamOf builds a Stream from a Go 1.23 range-over-func iterator. The iterator
// is not run until the transport drains it.
func StreamOf[T any](seq iter.Seq[T]) Stream[T] { return Stream[T]{seq: seq} }

// StreamSlice builds a Stream from a ready slice — convenient when the whole
// result set is already in memory but you still want NDJSON framing.
func StreamSlice[T any](items []T) Stream[T] {
	return Stream[T]{seq: func(yield func(T) bool) {
		for _, it := range items {
			if !yield(it) {
				return
			}
		}
	}}
}

// ForEach implements StreamProducer, erasing T to any for the transport.
func (s Stream[T]) ForEach(yield func(v any) bool) {
	if s.seq == nil {
		return
	}
	for v := range s.seq {
		if !yield(v) {
			return
		}
	}
}

// streamProducerType is the StreamProducer interface type, cached for
// buildEntry's return-kind check.
var streamProducerType = reflect.TypeFor[StreamProducer]()

// streamElem extracts T from an rpc.Stream[T] reflect.Type via its
// iter.Seq[T] field (func(func(T) bool)), so describe/codegen can present the
// STREAMED item type rather than the Stream wrapper. Returns false for anything
// that isn't a Stream.
func streamElem(t reflect.Type) (reflect.Type, bool) {
	if t.Kind() != reflect.Struct {
		return nil, false
	}
	f, ok := t.FieldByName("seq")
	if !ok {
		return nil, false
	}
	ft := f.Type // iter.Seq[T] == func(func(T) bool)
	if ft.Kind() != reflect.Func || ft.NumIn() != 1 {
		return nil, false
	}
	yield := ft.In(0) // func(T) bool
	if yield.Kind() != reflect.Func || yield.NumIn() != 1 {
		return nil, false
	}
	return yield.In(0), true
}
