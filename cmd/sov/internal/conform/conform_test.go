package conform

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// compliantPod stands up an httptest server that serves the minimum pod
// surface: /rpc/_introspect, /rpc/_health, and one RPC method.
func compliantPod(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc/_introspect", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"services":{"Widget":[{"router":"Widget","title":"Widget","methods":[`+
			`{"method":"get","title":"Get","postPath":"/rpc/Widget/get","hasParams":true,`+
			`"params":[{"jsonName":"id","schemaType":"string","required":true,"position":0}],`+
			`"requestTypeScript":"","responseTypeScript":""}]}]},"types":{},"cross_refs":{}}`)
	})
	mux.HandleFunc("/rpc/_health", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"status":"healthy","checked_at":"2026-01-01T00:00:00Z","gateway":{"status":"healthy"},"services":{"Widget":{"status":"healthy","local":true}}}`)
	})
	mux.HandleFunc("/rpc/Widget/get", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"data":{"id":"w1"}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestConform_CompliantPodPasses(t *testing.T) {
	srv := compliantPod(t)
	var out strings.Builder
	rc := Run([]string{"--pod", srv.URL, "--name", "Widget", "--method", "get", "--args", `{"id":"w1"}`}, &out, io.Discard)
	if rc != 0 {
		t.Fatalf("expected exit 0, got %d\n%s", rc, out.String())
	}
	if !strings.Contains(out.String(), "0 failed") {
		t.Fatalf("expected no failures, got:\n%s", out.String())
	}
}

func TestConform_MissingServiceFails(t *testing.T) {
	srv := compliantPod(t)
	var out strings.Builder
	// Ask for a service the pod doesn't declare → introspect check fails.
	rc := Run([]string{"--pod", srv.URL, "--name", "Nonexistent"}, &out, io.Discard)
	if rc == 0 {
		t.Fatalf("expected non-zero exit for absent service\n%s", out.String())
	}
}

func TestConform_RejectsBadEnvelopeShape(t *testing.T) {
	// Pod that 400s on the array arg shape → round-trip check must fail.
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc/_introspect", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"services":{"Widget":[{"router":"Widget","title":"Widget","methods":[`+
			`{"method":"get","title":"Get","postPath":"/rpc/Widget/get","hasParams":true,"params":[],`+
			`"requestTypeScript":"","responseTypeScript":""}]}]},"types":{},"cross_refs":{}}`)
	})
	mux.HandleFunc("/rpc/_health", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"status":"healthy","checked_at":"2026-01-01T00:00:00Z","gateway":{"status":"healthy"},"services":{}}`)
	})
	mux.HandleFunc("/rpc/Widget/get", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "[") { // rejects the array shape
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"array unsupported","code":"BAD_REQUEST"}}`)
			return
		}
		io.WriteString(w, `{"data":{}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var out strings.Builder
	rc := Run([]string{"--pod", srv.URL, "--name", "Widget", "--method", "get"}, &out, io.Discard)
	if rc == 0 {
		t.Fatalf("expected non-zero exit when pod rejects an arg shape\n%s", out.String())
	}
	if !strings.Contains(out.String(), "rejected the envelope shape") {
		t.Fatalf("expected envelope-shape failure, got:\n%s", out.String())
	}
}
