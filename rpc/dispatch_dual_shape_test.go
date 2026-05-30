package rpc

import (
	"strings"
	"testing"
)

type DualRouter struct{}

// Both sov: and json: tags. sov: drives dispatch (positional + named
// decode + required + omitempty). json: drives Go's stdlib marshaller
// for the response envelope. Keep them consistent — a small one-time
// duplication that lets the test compare bytes directly.
type DualParams struct {
	Name        string   `sov:"name,0,required" json:"name"`
	OwnerID     string   `sov:"owner_id,1" json:"owner_id"`
	Tags        []string `sov:"tags,2,omitempty" json:"tags,omitempty"`
	Description string   `sov:"description,3" json:"description"`
}

func (r *DualRouter) Make(_ *Context, p *DualParams) (*DualParams, error) {
	return p, nil
}

func newDualEngine(t *testing.T) *Engine {
	t.Helper()
	e := NewEngine()
	e.Register(&DualRouter{})
	return e
}

func TestDispatch_PositionalShape(t *testing.T) {
	e := newDualEngine(t)
	status, body := e.Dispatch(NewContext(nil), "Dual", "make",
		[]byte(`{"args":["foo","u1",["t1","t2"],"desc"]}`))
	if status != 200 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	want := `"name":"foo","owner_id":"u1","tags":["t1","t2"],"description":"desc"`
	if !strings.Contains(string(body), want) {
		t.Fatalf("body=%s", body)
	}
}

func TestDispatch_NamedShape(t *testing.T) {
	e := newDualEngine(t)
	status, body := e.Dispatch(NewContext(nil), "Dual", "make",
		[]byte(`{"args":{"name":"foo","owner_id":"u1","tags":["t1","t2"],"description":"desc"}}`))
	if status != 200 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	want := `"name":"foo","owner_id":"u1","tags":["t1","t2"],"description":"desc"`
	if !strings.Contains(string(body), want) {
		t.Fatalf("body=%s", body)
	}
}

func TestDispatch_LegacyArrayWithObject(t *testing.T) {
	e := newDualEngine(t)
	status, body := e.Dispatch(NewContext(nil), "Dual", "make",
		[]byte(`{"args":[{"name":"foo","owner_id":"u1"}]}`))
	if status != 200 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if !strings.Contains(string(body), `"name":"foo"`) {
		t.Fatalf("body=%s", body)
	}
}

func TestDispatch_NamedAndPositionalEquivalent(t *testing.T) {
	e := newDualEngine(t)
	_, posBody := e.Dispatch(NewContext(nil), "Dual", "make",
		[]byte(`{"args":["foo","u1",["t1","t2"],"desc"]}`))
	_, nameBody := e.Dispatch(NewContext(nil), "Dual", "make",
		[]byte(`{"args":{"name":"foo","owner_id":"u1","tags":["t1","t2"],"description":"desc"}}`))
	if string(posBody) != string(nameBody) {
		t.Fatalf("positional vs named diverged:\n  pos:  %s\n  name: %s", posBody, nameBody)
	}
}

func TestDispatch_RequiredMissing_Named(t *testing.T) {
	e := newDualEngine(t)
	status, body := e.Dispatch(NewContext(nil), "Dual", "make",
		[]byte(`{"args":{"owner_id":"u1"}}`))
	if status != 400 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if !strings.Contains(string(body), "name") || !strings.Contains(string(body), "required") {
		t.Fatalf("body=%s", body)
	}
}

func TestDispatch_RequiredMissing_Positional(t *testing.T) {
	e := newDualEngine(t)
	status, body := e.Dispatch(NewContext(nil), "Dual", "make",
		[]byte(`{"args":[]}`))
	if status != 400 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if !strings.Contains(string(body), "name") || !strings.Contains(string(body), "required") {
		t.Fatalf("body=%s", body)
	}
}

func TestDispatch_TooManyPositional(t *testing.T) {
	e := newDualEngine(t)
	status, body := e.Dispatch(NewContext(nil), "Dual", "make",
		[]byte(`{"args":["foo","u1",["t1"],"desc","extra"]}`))
	if status != 400 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if !strings.Contains(string(body), "too many positional") {
		t.Fatalf("body=%s", body)
	}
}

func TestDispatch_UnknownKeysIgnored(t *testing.T) {
	e := newDualEngine(t)
	status, body := e.Dispatch(NewContext(nil), "Dual", "make",
		[]byte(`{"args":{"name":"foo","owner_id":"u1","mystery":"future-field"}}`))
	if status != 200 {
		t.Fatalf("status=%d body=%s — unknown keys must be ignored for forward compat", status, body)
	}
}

func TestDispatch_BadShape(t *testing.T) {
	e := newDualEngine(t)
	status, _ := e.Dispatch(NewContext(nil), "Dual", "make",
		[]byte(`{"args":"not-array-not-object"}`))
	if status != 400 {
		t.Fatalf("status=%d", status)
	}
}
