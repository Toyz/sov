package registry

import "testing"

// Regression: a pod whose every method is hard-hidden returns a valid
// IntrospectReport with an EMPTY services map. The decoder must accept it
// (contributing nothing) rather than falling through to the bare-array
// path and erroring — which logged a spurious aggregator warn.
func TestDecodeIntrospectBody(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
		wantSvc int
	}{
		{"empty report (all methods hidden)", `{"services":{},"types":{},"cross_refs":{}}`, false, 0},
		{"bare empty object", `{}`, false, 0},
		{"report with a service", `{"services":{"Chirp":[{"router":"Chirp","title":"Chirp","methods":[]}]}}`, false, 1},
		{"legacy bare array", `[{"router":"Auth","title":"Auth","methods":[]}]`, false, 1},
		{"empty array", `[]`, false, 0},
		{"leading whitespace object", "  \n{\"services\":{}}", false, 0},
		{"garbage", `not json`, true, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, err := decodeIntrospectBody([]byte(c.body))
			if c.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr = %v", err, c.wantErr)
			}
			if err == nil && len(svc) != c.wantSvc {
				t.Fatalf("services = %d, want %d", len(svc), c.wantSvc)
			}
		})
	}
}
