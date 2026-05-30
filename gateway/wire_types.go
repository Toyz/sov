package gateway

import (
	"encoding/json"
)

// ---------------------------------------------------------------------------
// _register
// ---------------------------------------------------------------------------

// RegisterRequest is the JSON body of POST /rpc/_register.
//
// Auth+Verify, when both set, tell the gateway "this service is the
// auth verifier — route bearer tokens here." Authz+Check tell the
// gateway "this service is the policy-as-service hook." A service may
// declare zero, one, or both roles; non-role services just expose their
// own business methods.
//
// Introspect, when true, opts the pod into the gateway's
// /_introspect aggregation: the gateway will probe this pod's
// /rpc/_introspect on catalog rebuild and merge its descriptors into
// the org-wide type catalog. Defaults to false on the wire so that
// services opt in explicitly; the chirp mesh demo sets it true.
type RegisterRequest struct {
	Name              string `json:"name"`
	Address           string `json:"address"`
	HeartbeatInterval int    `json:"heartbeat_interval_seconds"`
	Auth              bool   `json:"auth,omitempty"`
	Verify            string `json:"verify,omitempty"`
	Authz             bool   `json:"authz,omitempty"`
	Check             string `json:"check,omitempty"`
	Introspect        bool   `json:"introspect,omitempty"`
	// Federate, when true, advertises this gateway as a tiered router
	// that fronts every name in Services. Master registers each name
	// at Address. Name in this mode is a label, not a wire name.
	Federate bool     `json:"federate,omitempty"`
	Services []string `json:"services,omitempty"`
}

// RegisterResponse is the success body. ForceIntrospect, when true,
// signals the pod that the gateway has the explorer plugin (or other
// catalog consumer) installed and the pod's entry was force-flipped
// to Introspectable=true server-side regardless of what the pod
// requested. The pod SHOULD update its in-memory mesh options so
// future heartbeats / re-registers stay consistent.
type RegisterResponse struct {
	OK              bool `json:"ok"`
	TTL             int  `json:"ttl_seconds"`
	ForceIntrospect bool `json:"force_introspect,omitempty"`
}

// ---------------------------------------------------------------------------
// _batch
// ---------------------------------------------------------------------------

// BatchRequest is the JSON body of POST /rpc/_batch.
type BatchRequest struct {
	Calls map[string]BatchCall `json:"calls"`
}

// BatchCall is one entry in a batch — caller-supplied alias keys the
// result map symmetrically.
type BatchCall struct {
	Service string          `json:"service"`
	Method  string          `json:"method"`
	Args    json.RawMessage `json:"args,omitempty"` // forwarded verbatim as {"args": <args>}
}

// BatchResponse is the success body.
type BatchResponse struct {
	Results map[string]json.RawMessage `json:"results"`
}

// handleBatch parses the BatchRequest, groups entries by resolved
// destination, then dispatches each group concurrently. Remote groups
// with ≥2 entries POST one nested /rpc/_batch to the destination pod
// (with auto-fallback to per-entry dispatch on 404); other buckets
// run today's per-entry concurrent fan-out. Caller-facing
// BatchResponse shape is unchanged.
