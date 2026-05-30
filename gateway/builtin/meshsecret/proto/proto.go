// Package proto is the wire format for the mesh-secret-gated
// /rpc/_register sig. Pure helpers: no gateway dependency. The
// meshsecret plugin uses Verify on the registry side; the pod-side
// heartbeat (gateway/mesh.go) uses Sign on every heartbeat POST.
package proto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// RegisterTsHeader is the wall-clock timestamp (unix seconds) the pod
// stamps on its _register POST. Registry rejects anything outside
// SkewWindow of its own clock so replays die fast.
const RegisterTsHeader = "X-Sov-Register-Ts"

// RegisterSigHeader is the HMAC-SHA256 hex signature over the
// canonical message (see canonicalMessage). Registry recomputes and
// constant-time-compares.
const RegisterSigHeader = "X-Sov-Register-Sig"

// SkewWindow bounds the acceptable drift between pod clock and
// registry clock on _register POSTs.
const SkewWindow = 5 * time.Minute

// canonicalMessage builds the bytes hashed into the HMAC. The scheme
// matches sov/signing's v2 message for data-plane requests but uses a
// distinct domain string ("register") so cross-use of the same secret
// across the control and data planes is harmless: a sig made for one
// cannot validate against the other.
//
// Format (newline-terminated):
//
//	v1\n
//	register\n
//	<sha256_hex(body)>\n
//	<unix_ts>\n
func canonicalMessage(body []byte, ts int64) []byte {
	sum := sha256.Sum256(body)
	bodyHex := hex.EncodeToString(sum[:])
	return []byte(fmt.Sprintf("v1\nregister\n%s\n%d\n", bodyHex, ts))
}

// Sign returns (sigHex, tsStr) for the given body. Pods call this once
// per heartbeat (timestamp moves) and stamp the result on the outbound
// request as RegisterSigHeader / RegisterTsHeader.
func Sign(secret, body []byte, now time.Time) (sigHex, tsStr string) {
	ts := now.UTC().Unix()
	canonical := canonicalMessage(body, ts)
	mac := hmac.New(sha256.New, secret)
	mac.Write(canonical)
	return hex.EncodeToString(mac.Sum(nil)), strconv.FormatInt(ts, 10)
}

// Verify validates the (sig, ts) pair against the body under the
// secret. Returns nil on success; a descriptive error on failure
// (missing args, bad hex, bad timestamp, out-of-window, signature
// mismatch). now is injected for tests.
func Verify(secret []byte, sig, ts string, body []byte, now time.Time) error {
	if sig == "" || ts == "" {
		return errors.New("missing X-Sov-Register-Sig or X-Sov-Register-Ts header")
	}
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("X-Sov-Register-Ts is not a unix timestamp: %w", err)
	}
	drift := now.UTC().Unix() - tsInt
	if drift < 0 {
		drift = -drift
	}
	if time.Duration(drift)*time.Second > SkewWindow {
		return fmt.Errorf("X-Sov-Register-Ts %d outside ±%s skew window", tsInt, SkewWindow)
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("X-Sov-Register-Sig is not valid hex: %w", err)
	}
	canonical := canonicalMessage(body, tsInt)
	mac := hmac.New(sha256.New, secret)
	mac.Write(canonical)
	want := mac.Sum(nil)
	if !hmac.Equal(got, want) {
		return errors.New("signature does not match canonical message")
	}
	return nil
}
