// Package catalog centralizes the shared sovgen plumbing: fetching a
// /rpc/_introspect report, spawning a gateway binary on a free local
// port, polling for readiness, and the repeated --header K=V flag
// type. Each language-specific subcommand (ts, go, kotlin, swift,
// python) imports this so the wire interface and retry/timeout
// behavior stays identical across emitters.
//
// Behavior preserved from the original duplicated copies in
// cmd/sovgen/ts/ts.go and cmd/sovgen/golang/golang.go:
//   - introspect fetch is POST /rpc/_introspect with a 15s timeout
//   - SOV_LISTEN is the env var the spawned binary must honor
//   - waitReady polls every 100ms until the endpoint answers <500
//   - StringSliceFlag is the same K=V-comma-joined --header shape
package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Toyz/sov/gateway"
)

// Fetch POSTs /rpc/_introspect on base and decodes the IntrospectReport.
// headers are passed through verbatim (K=V pairs, same shape as the
// repeatable --header flag).
func Fetch(base string, headers []string) (*gateway.IntrospectReport, error) {
	return FetchPath[gateway.IntrospectReport](base, "/rpc/_introspect", headers)
}

// FetchPath POSTs path on base, passes headers through verbatim (K=V),
// and decodes the JSON response into *T. It treats any status >= 400 as
// a hard error (reading the body into the message). Callers that need
// to decode non-2xx bodies (e.g. the 207/503 health rollup) must hit
// the endpoint themselves.
func FetchPath[T any](base, path string, headers []string) (*T, error) {
	url := strings.TrimRight(base, "/") + path
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return nil, err
	}
	for _, h := range headers {
		k, v, ok := strings.Cut(h, "=")
		if !ok {
			return nil, fmt.Errorf("invalid --header %q (want K=V)", h)
		}
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		// Label mirrors the old per-command messages ("decode
		// introspect", "decode health"): last path segment, "_" stripped.
		label := path
		if i := strings.LastIndex(label, "/"); i >= 0 {
			label = label[i+1:]
		}
		label = strings.TrimPrefix(label, "_")
		return nil, fmt.Errorf("decode %s: %w", label, err)
	}
	return &out, nil
}

// ErrSourceUsage is returned by ResolveSource when neither or both of
// from/execBin are set. Callers test for it (errors.Is) to choose
// between a usage error (print + usage, exit 2) and a spawn failure
// (print, exit 1).
var ErrSourceUsage = errors.New("exactly one of --from or --exec is required")

// ResolveSource validates the standard --from / --exec source pair and
// returns the base URL to fetch from plus a cleanup the caller MUST
// defer. Exactly one of from/execBin must be set:
//   - neither or both → ErrSourceUsage (callers print it + usage, exit 2)
//   - execBin set     → Spawn the binary, return its url + kill-cleanup
//     (Spawn failure surfaces as a non-ErrSourceUsage error → exit 1)
//   - from set        → return (from, no-op cleanup, nil)
func ResolveSource(from, execBin string, execTimeout time.Duration, stderr io.Writer) (string, func(), error) {
	noop := func() {}
	if (from == "" && execBin == "") || (from != "" && execBin != "") {
		return "", noop, ErrSourceUsage
	}
	if execBin != "" {
		url, cleanup, err := Spawn(execBin, execTimeout, stderr)
		if err != nil {
			return "", noop, err
		}
		return url, cleanup, nil
	}
	return from, noop, nil
}

// Spawn launches the given binary on a free local port (passed via
// SOV_LISTEN), polls /rpc/_introspect until it answers, and returns
// the URL plus a cleanup that kills the child. The binary must
// respect SOV_LISTEN to bind the port we picked.
func Spawn(bin string, timeout time.Duration, stderr io.Writer) (string, func(), error) {
	port, err := PickFreePort()
	if err != nil {
		return "", nil, fmt.Errorf("pick port: %w", err)
	}
	addr := ":" + strconv.Itoa(port)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "SOV_LISTEN="+addr)
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("start %s: %w", bin, err)
	}
	cleanup := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}
	url := "http://localhost:" + strconv.Itoa(port)
	if err := WaitReady(url, timeout); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("wait ready: %w", err)
	}
	return url, cleanup, nil
}

// PickFreePort asks the kernel for an unused TCP port, closes the
// listener, and returns the number. Standard race-free port-picker
// idiom; the spawned binary takes it microseconds later.
func PickFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// WaitReady polls /rpc/_introspect every 100ms until it answers <500
// or timeout elapses.
func WaitReady(base string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := base + "/rpc/_introspect"
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodPost, url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout after %s waiting for %s", timeout, url)
}

// StringSliceFlag accepts repeated --header K=V flags.
type StringSliceFlag []string

func (s *StringSliceFlag) String() string     { return strings.Join(*s, ",") }
func (s *StringSliceFlag) Set(v string) error { *s = append(*s, v); return nil }
