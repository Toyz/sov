package rpc

import (
	"context"
	"strings"
	"testing"
)

type greetParams struct {
	Name string `json:"name"`
}

func TestHandle_TypedDispatch_BothArgShapes(t *testing.T) {
	e := NewEngine()
	Handle(e, "Greeter", "hi", func(_ *Context, p *greetParams) (map[string]string, error) {
		return map[string]string{"hi": p.Name}, nil
	})

	for _, tc := range []struct {
		shape string
		body  string
	}{
		{"named", `{"args":{"name":"sov"}}`},
		{"positional", `{"args":["sov"]}`},
		{"object-in-array", `{"args":[{"name":"sov"}]}`},
	} {
		st, body := e.Dispatch(NewContext(context.Background()), "Greeter", "hi", []byte(tc.body))
		if st != 200 {
			t.Fatalf("%s: status=%d body=%s", tc.shape, st, body)
		}
		if !strings.Contains(string(body), `"hi":"sov"`) {
			t.Errorf("%s: body=%s, want hi=sov", tc.shape, body)
		}
	}
}

func TestHandle_ErrorPath(t *testing.T) {
	e := NewEngine()
	HandleErr(e, "Greeter", "boom", func(_ *Context, _ *greetParams) error {
		return BadRequest("nope")
	})
	st, body := e.Dispatch(NewContext(context.Background()), "Greeter", "boom", []byte(`{"args":{}}`))
	if st != 400 {
		t.Fatalf("status=%d body=%s, want 400", st, body)
	}
	if !strings.Contains(string(body), "nope") {
		t.Errorf("body=%s, want 'nope'", body)
	}
}

func TestHandle_AppearsInDescribe(t *testing.T) {
	e := NewEngine()
	Handle(e, "Greeter", "hi", func(_ *Context, p *greetParams) (map[string]string, error) {
		return nil, nil
	})
	var found *MethodDescriptor
	for _, rd := range e.Describe() {
		if rd.Router != "Greeter" {
			continue
		}
		for i := range rd.Methods {
			if rd.Methods[i].Method == "hi" {
				found = &rd.Methods[i]
			}
		}
	}
	if found == nil {
		t.Fatal("typed method missing from Describe()")
	}
	if !found.HasParams || len(found.Params) != 1 || found.Params[0].JSONName != "name" {
		t.Errorf("descriptor params wrong: %+v", found.Params)
	}
}

func TestHandle_DupPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate typed method")
		}
	}()
	e := NewEngine()
	Handle(e, "Greeter", "hi", func(_ *Context, _ *greetParams) (int, error) { return 0, nil })
	Handle(e, "Greeter", "hi", func(_ *Context, _ *greetParams) (int, error) { return 0, nil })
}

// BenchmarkHandleTypedDispatch is the reflection-free counterpart to
// BenchmarkEngineDispatchLocal — compare ns/op and allocs/op.
func BenchmarkHandleTypedDispatch(b *testing.B) {
	e := NewEngine()
	Handle(e, "Bench", "greet", func(_ *Context, p *greetParams) (map[string]string, error) {
		return map[string]string{"hello": p.Name}, nil
	})
	body := []byte(`{"args":{"name":"sov"}}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := NewContext(context.Background())
		if st, _ := e.Dispatch(ctx, "Bench", "greet", body); st != 200 {
			b.Fatalf("status %d", st)
		}
	}
}
