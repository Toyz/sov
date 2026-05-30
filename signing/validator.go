package signing

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"strconv"
	"time"
)

// Reason is a stable code returned alongside a validation failure so
// callers can map it to an HTTP status / wire error code.
type Reason string

const (
	ReasonMissingHeaders Reason = "MISSING_HEADERS"
	ReasonBadTimestamp   Reason = "BAD_TIMESTAMP"
	ReasonExpired        Reason = "TIMESTAMP_EXPIRED"
	ReasonBadSignatureFmt Reason = "BAD_SIGNATURE_FORMAT"
	ReasonSessionMissing Reason = "SESSION_EXPIRED"
	ReasonSessionLookup  Reason = "SESSION_LOOKUP_FAILED"
	ReasonBadPublicKey   Reason = "BAD_PUBLIC_KEY"
	ReasonInvalidSig     Reason = "INVALID_SIGNATURE"
)

// Failure is the error type the Validator returns on any rejection.
type Failure struct {
	Reason  Reason
	Message string
	Err     error
}

func (f *Failure) Error() string {
	if f.Err != nil {
		return f.Message + ": " + f.Err.Error()
	}
	return f.Message
}
func (f *Failure) Unwrap() error { return f.Err }

// Options configures a Validator.
type Options struct {
	// Store is required; resolves session id → public key.
	Store PublicKeyStore
	// ReplayWindow is how far the timestamp may drift from now. Default 30s.
	ReplayWindow time.Duration
	// Now overrides time.Now for tests.
	Now func() time.Time
}

// Validator validates per-request Ed25519 signatures.
type Validator struct {
	store        PublicKeyStore
	replayWindow time.Duration
	now          func() time.Time
}

// New returns a Validator. Store is mandatory.
func New(opts Options) *Validator {
	if opts.ReplayWindow <= 0 {
		opts.ReplayWindow = 30 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Validator{store: opts.Store, replayWindow: opts.ReplayWindow, now: opts.Now}
}

// Headers is the subset of inbound HTTP headers the validator reads.
// Pulled out as a tiny type so callers can adapt fiber/fasthttp/net/http
// or any other source.
type Headers interface {
	Get(name string) string
}

// Validate checks the X-Session / X-Ts / X-Sig headers against the
// canonical message derived from router / method / body. On success it
// returns the session id (so callers can use it for follow-up lookups
// like "who is this session's user?"). On failure it returns a *Failure.
func (v *Validator) Validate(ctx context.Context, hdr Headers, router, method string, body []byte) (sessionID string, err error) {
	if v.store == nil {
		return "", &Failure{Reason: ReasonSessionLookup, Message: "signing: validator has no PublicKeyStore"}
	}
	sid := hdr.Get("X-Session")
	ts := hdr.Get("X-Ts")
	sigHex := hdr.Get("X-Sig")
	if sid == "" || ts == "" || sigHex == "" {
		return "", &Failure{Reason: ReasonMissingHeaders, Message: "missing X-Session, X-Ts, or X-Sig"}
	}

	tsInt, parseErr := strconv.ParseInt(ts, 10, 64)
	if parseErr != nil {
		return "", &Failure{Reason: ReasonBadTimestamp, Message: "X-Ts is not a unix timestamp", Err: parseErr}
	}
	drift := v.now().UTC().Unix() - tsInt
	if drift < 0 {
		drift = -drift
	}
	if time.Duration(drift)*time.Second > v.replayWindow {
		return "", &Failure{Reason: ReasonExpired, Message: "request timestamp outside replay window"}
	}

	sig, decErr := hex.DecodeString(sigHex)
	if decErr != nil {
		return "", &Failure{Reason: ReasonBadSignatureFmt, Message: "X-Sig is not valid hex", Err: decErr}
	}
	if len(sig) != ed25519.SignatureSize {
		return "", &Failure{Reason: ReasonBadSignatureFmt, Message: "X-Sig has wrong length"}
	}

	pub, lookupErr := v.store.Get(ctx, sid)
	if lookupErr != nil {
		if errors.Is(lookupErr, ErrSessionNotFound) {
			return "", &Failure{Reason: ReasonSessionMissing, Message: "session expired or unknown"}
		}
		return "", &Failure{Reason: ReasonSessionLookup, Message: "session store lookup failed", Err: lookupErr}
	}
	if len(pub) != ed25519.PublicKeySize {
		return "", &Failure{Reason: ReasonBadPublicKey, Message: "session has invalid public key"}
	}

	msg := CanonicalMessage(router, method, body, tsInt)
	if !ed25519.Verify(pub, msg, sig) {
		return "", &Failure{Reason: ReasonInvalidSig, Message: "signature does not match canonical message"}
	}
	return sid, nil
}
