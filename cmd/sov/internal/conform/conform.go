// Package conform implements `sov conform` — validate that a running pod
// (in ANY language) satisfies the sov wire contract (docs/WIRE_CONTRACT.md).
//
// It exercises the contract end to end against a live pod URL: the served
// /rpc/_introspect + /rpc/_health, a dual-arg-shape RPC round-trip, and
// (optionally) X-Sov-Seal handling. With --gateway it also confirms the pod
// actually registered (proving its outbound register+signing worked) and,
// with --mesh-secret, directly probes the register signing contract.
//
// Note on roles: a pod SERVES /rpc/{name}/{method}, /rpc/_introspect and
// /rpc/_health, and CALLS the gateway's /rpc/_register — pods do not serve
// _register themselves. conform checks each obligation on the right side.
//
// Each check prints PASS/FAIL; the command exits non-zero if any check
// fails, so it doubles as a CI gate for polyglot pods.
package conform

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Toyz/sov/cmd/sov/internal/catalog"
	"github.com/Toyz/sov/gateway"
	hmacproto "github.com/Toyz/sov/gateway/builtin/hmacseal/proto"
	meshproto "github.com/Toyz/sov/gateway/builtin/meshsecret/proto"
)

// Run executes the conform subcommand. Returns a process exit code.
func Run(argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sov conform", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pod := fs.String("pod", "", "pod base URL to validate, e.g. http://localhost:9002 (required)")
	name := fs.String("name", "", "the router/service wire name the pod serves, e.g. Chirp (required)")
	gw := fs.String("gateway", "", "gateway base URL; when set, also verify the pod registered + (with --mesh-secret) probe the register signing contract")
	meshSecret := fs.String("mesh-secret", "", "mesh secret; with --gateway, sends a signed /rpc/_register probe to validate the signing canon")
	hmacSecret := fs.String("hmac-secret", "", "seal secret; when set, the seal round-trip check runs against the pod")
	method := fs.String("method", "", "method to round-trip; defaults to the first method in the pod's introspect")
	argsJSON := fs.String("args", "{}", "named args object for the round-trip, e.g. '{\"body\":\"hi\"}'")
	var headers catalog.StringSliceFlag
	fs.Var(&headers, "header", "extra header on every request, K=V; repeatable")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	if *pod == "" || *name == "" {
		fmt.Fprintln(stderr, "sov conform: --pod and --name are required")
		fs.Usage()
		return 2
	}

	c := &checker{
		base: strings.TrimRight(*pod, "/"), name: *name,
		gateway: strings.TrimRight(*gw, "/"), meshSecret: *meshSecret, hmacSecret: *hmacSecret,
		method: *method, argsJSON: *argsJSON, headers: headers, out: stdout,
		client: &http.Client{Timeout: 10 * time.Second},
	}

	report := c.checkIntrospect()
	c.checkHealth()
	c.checkRPCRoundTrip(report)
	if *hmacSecret != "" {
		c.checkSeal()
	}
	if c.gateway != "" {
		c.checkRegistered()
		if *meshSecret != "" {
			c.checkSigningContract()
		}
	}

	fmt.Fprintf(stdout, "\n%d passed, %d failed\n", c.passed, c.failed)
	if c.failed > 0 {
		return 1
	}
	return 0
}

type checker struct {
	base, name             string
	gateway                string
	meshSecret, hmacSecret string
	method, argsJSON       string
	headers                []string
	client                 *http.Client
	out                    io.Writer
	passed, failed         int
}

func (c *checker) pass(msg string) { c.passed++; fmt.Fprintf(c.out, "  PASS  %s\n", msg) }
func (c *checker) fail(msg string, a ...any) {
	c.failed++
	fmt.Fprintf(c.out, "  FAIL  %s\n", fmt.Sprintf(msg, a...))
}

func (c *checker) applyHeaders(req *http.Request) {
	for _, h := range c.headers {
		if k, v, ok := strings.Cut(h, "="); ok {
			req.Header.Set(strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}
}

// 1. The pod SERVES /rpc/_introspect and lists its service with methods.
func (c *checker) checkIntrospect() *gateway.IntrospectReport {
	fmt.Fprintln(c.out, "pod /rpc/_introspect:")
	report, err := catalog.Fetch(c.base, c.headers)
	if err != nil {
		c.fail("fetch/decode: %v", err)
		return nil
	}
	rds, ok := report.Services[c.name]
	if !ok || len(rds) == 0 {
		c.fail("service %q absent from introspect.services", c.name)
		return report
	}
	nMethods := 0
	for _, rd := range rds {
		nMethods += len(rd.Methods)
	}
	if nMethods == 0 {
		c.fail("service %q has no methods", c.name)
		return report
	}
	c.pass(fmt.Sprintf("service %q present with %d method(s)", c.name, nMethods))
	return report
}

// 2. The pod SERVES /rpc/_health with a valid status + matching HTTP code.
func (c *checker) checkHealth() {
	fmt.Fprintln(c.out, "pod /rpc/_health:")
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, c.base+"/rpc/_health", nil)
	c.applyHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		c.fail("GET /rpc/_health: %v", err)
		return
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	var hr gateway.HealthReport
	if err := json.Unmarshal(rb, &hr); err != nil {
		c.fail("decode HealthReport: %v", err)
		return
	}
	valid := map[string]bool{"healthy": true, "degraded": true, "unhealthy": true, "unknown": true, "missing": true}
	if !valid[hr.Status] {
		c.fail("status %q not in taxonomy", hr.Status)
		return
	}
	wantHTTP := map[string]int{"healthy": 200, "degraded": 207, "unhealthy": 503}
	if want, ok := wantHTTP[hr.Status]; ok && resp.StatusCode != want {
		c.fail("status %q but HTTP %d (want %d)", hr.Status, resp.StatusCode, want)
		return
	}
	c.pass(fmt.Sprintf("status=%q http=%d", hr.Status, resp.StatusCode))
}

// 3. The pod accepts BOTH arg shapes on /rpc/{name}/{method}.
func (c *checker) checkRPCRoundTrip(report *gateway.IntrospectReport) {
	fmt.Fprintln(c.out, "pod rpc round-trip:")
	method := c.resolveMethod(report)
	if method == "" {
		c.fail("no method to round-trip (pass --method)")
		return
	}
	var argsObj json.RawMessage
	if err := json.Unmarshal([]byte(c.argsJSON), &argsObj); err != nil {
		c.fail("--args is not valid JSON: %v", err)
		return
	}
	named, _ := json.Marshal(map[string]json.RawMessage{"args": argsObj})          // {"args":{...}}
	positional, _ := json.Marshal(map[string][]json.RawMessage{"args": {argsObj}}) // {"args":[{...}]}

	// A round-trip proves the pod ACCEPTS the envelope shape — not that the
	// call is authorized. 200 is ideal; 401/403 means the shape parsed and
	// routed but the method is auth/authz-gated (fine — pass --token/seal to
	// exercise the happy path). Only 400/404/5xx mean the envelope itself was
	// rejected, which is a contract violation.
	for _, tc := range []struct {
		shape string
		body  []byte
	}{
		{"named {args:{}}", named},
		{"array {args:[{}]}", positional},
	} {
		status, data, err := c.callMethod(method, tc.body, nil)
		switch {
		case err != nil:
			c.fail("%s %s: %v", method, tc.shape, err)
		case status == http.StatusOK && data:
			c.pass(fmt.Sprintf("%s accepts %s (200)", method, tc.shape))
		case status == http.StatusOK && !data:
			c.fail("%s %s: 200 but response has no \"data\" key", method, tc.shape)
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			c.pass(fmt.Sprintf("%s accepts %s (shape ok; method gated, http=%d)", method, tc.shape, status))
		case status == http.StatusBadRequest || status == http.StatusNotFound:
			c.fail("%s %s: HTTP %d — pod rejected the envelope shape", method, tc.shape, status)
		default:
			c.fail("%s %s: unexpected HTTP %d", method, tc.shape, status)
		}
	}
}

// 4. Seal smoke check: a validly-sealed identity bundle must not error.
func (c *checker) checkSeal() {
	fmt.Fprintln(c.out, "pod x-sov-seal:")
	if c.method == "" {
		c.fail("seal check needs --method")
		return
	}
	body, _ := json.Marshal(map[string]json.RawMessage{"args": json.RawMessage(c.argsJSON)})
	h := http.Header{}
	h.Set("X-Sov-Subject", "conform-probe")
	h.Set("X-Sov-Issuer", "sov-conform")
	h.Set(hmacproto.HeaderSeal, hmacproto.Sign(h, []byte(c.hmacSecret)))

	status, _, err := c.callMethod(c.method, body, h)
	switch {
	case err != nil:
		c.fail("sealed request: %v", err)
	case status >= 500:
		c.fail("sealed request returned HTTP %d (pod erred on a valid seal)", status)
	default:
		c.pass(fmt.Sprintf("valid seal accepted (http=%d)", status))
	}
}

// 5. The pod registered: its name shows up in the gateway's /rpc/_health.
// Proves the pod's OUTBOUND register + signing succeeded (a bad signature
// would have been rejected and the name would be absent).
func (c *checker) checkRegistered() {
	fmt.Fprintln(c.out, "gateway registration:")
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, c.gateway+"/rpc/_health", nil)
	c.applyHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		c.fail("GET gateway /rpc/_health: %v", err)
		return
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	var hr gateway.HealthReport
	if err := json.Unmarshal(rb, &hr); err != nil {
		c.fail("decode gateway HealthReport: %v", err)
		return
	}
	if _, ok := hr.Services[c.name]; !ok {
		c.fail("service %q not registered at gateway (outbound register/sign failed?)", c.name)
		return
	}
	c.pass(fmt.Sprintf("service %q registered at gateway (status=%q)", c.name, hr.Services[c.name].Status))
}

// 6. Register-signing contract: send a signed /rpc/_register to the gateway
// and assert 200, validating the canonical-message + HMAC scheme directly.
func (c *checker) checkSigningContract() {
	fmt.Fprintln(c.out, "register signing contract:")
	body, _ := json.Marshal(gateway.RegisterRequest{
		Name: c.name, Address: c.base, HeartbeatInterval: 5, Introspect: true,
	})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, c.gateway+"/rpc/_register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.applyHeaders(req)
	sig, ts := meshproto.Sign([]byte(c.meshSecret), body, time.Now())
	req.Header.Set(meshproto.RegisterSigHeader, sig)
	req.Header.Set(meshproto.RegisterTsHeader, ts)
	resp, err := c.client.Do(req)
	if err != nil {
		c.fail("POST gateway /rpc/_register: %v", err)
		return
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		c.fail("signed register rejected: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(rb)))
		return
	}
	c.pass("gateway accepted a register signed per the contract")
}

func (c *checker) resolveMethod(report *gateway.IntrospectReport) string {
	if c.method != "" {
		return c.method
	}
	if report != nil {
		for _, rd := range report.Services[c.name] {
			if len(rd.Methods) > 0 {
				return rd.Methods[0].Method
			}
		}
	}
	return ""
}

// callMethod POSTs body to /rpc/{name}/{method} with optional extra
// identity headers; returns (status, hasDataKey, error).
func (c *checker) callMethod(method string, body []byte, extra http.Header) (int, bool, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		c.base+"/rpc/"+c.name+"/"+method, bytes.NewReader(body))
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.applyHeaders(req)
	for k, vs := range extra {
		for _, v := range vs {
			req.Header.Set(k, v)
		}
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	var env map[string]json.RawMessage
	_ = json.Unmarshal(rb, &env)
	_, hasData := env["data"]
	return resp.StatusCode, hasData, nil
}
