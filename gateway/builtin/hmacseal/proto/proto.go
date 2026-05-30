// Package proto is the wire format for X-Sov-Seal. Pure helpers: no
// gateway dependency, no http server state — just the constant, the
// HMAC compute, and the verify. The hmacseal plugin uses these on
// outbound injection; the framework trust guard uses them on inbound
// verification. Splitting the proto out lets both sides import the
// same canonical implementation without an import cycle.
package proto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
)

// HeaderSeal names the HMAC envelope written across the X-Sov-* claim
// bundle. Downstream services reject the bundle if the seal does not
// recompute under the agreed-upon mesh key.
const HeaderSeal = "X-Sov-Seal"

// Sign builds the HMAC-SHA256 hex seal over every X-Sov-* header
// (except HeaderSeal itself) in canonical order. Downstream services
// call the matching Verify and reject the request if the seals differ.
//
// Canonical form: sorted "header_name=value\n" lines, lowercased
// names, trailing newline. HMAC over the resulting bytes, hex-encoded.
func Sign(h http.Header, secret []byte) string {
	type kv struct{ k, v string }
	var pairs []kv
	sealLower := strings.ToLower(HeaderSeal)
	for name := range h {
		lname := strings.ToLower(name)
		if !strings.HasPrefix(lname, "x-sov-") || lname == sealLower {
			continue
		}
		pairs = append(pairs, kv{k: lname, v: h.Get(name)})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].k < pairs[j].k })
	var b strings.Builder
	for _, p := range pairs {
		b.WriteString(p.k)
		b.WriteByte('=')
		b.WriteString(p.v)
		b.WriteByte('\n')
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(b.String()))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify recomputes the seal over h with secret and compares against
// h.Get(HeaderSeal) in constant time. Returns true iff the seal
// matches. Downstream pods call this in middleware to reject forged
// claim headers from callers that bypass the gateway.
func Verify(h http.Header, secret []byte) bool {
	got := h.Get(HeaderSeal)
	if got == "" {
		return false
	}
	want := Sign(h, secret)
	gb, err := hex.DecodeString(got)
	if err != nil {
		return false
	}
	wb, err := hex.DecodeString(want)
	if err != nil {
		return false
	}
	return hmac.Equal(gb, wb)
}
