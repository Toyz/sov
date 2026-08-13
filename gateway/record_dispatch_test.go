package gateway_test

import (
	"testing"
	"time"

	. "github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/gwtest"
)

// RecordDispatch is exported for third-party surface authors who emit a
// dispatch event for a call made OUTSIDE the /rpc path. Its doc treats a nil
// req as a valid input (a synthetic/background event with no inbound request),
// and it is NOT wrapped by safeHook's recover — so a nil-req panic would kill
// the connection. subjectOf must nil-guard.
func TestRecordDispatch_NilRequestNoPanic(t *testing.T) {
	gw := gwtest.New()
	// Must not panic on a nil req.
	gw.RecordDispatch(nil, "Router", "method", "/rpc/Router/method", &Response{Status: 200}, time.Now())
}
