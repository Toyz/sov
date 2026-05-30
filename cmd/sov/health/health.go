// Package health implements `sov health` — pretty-print of the
// /rpc/_health rollup. Shows overall status + per-service status with
// color coding (green healthy / yellow degraded / red unhealthy or
// missing). Optional --watch loop for terminals where the operator
// wants a live dashboard.
//
// Same --from / --exec / --header flag shape as the other CLI
// subcommands. Spawned binaries must respect SOV_LISTEN.
package health

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Toyz/sov/cmd/sov/internal/catalog"
	"github.com/Toyz/sov/cmd/sov/internal/output"
	"github.com/Toyz/sov/gateway"
)

// Run executes the health subcommand.
func Run(argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sov health", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "", "gateway base URL; CLI fetches {from}/rpc/_health. Mutually exclusive with --exec.")
	execBin := fs.String("exec", "", "path to a sov gateway binary; spawns it on a free local port, fetches, kills. Honors SOV_LISTEN.")
	execTimeout := fs.Duration("exec-timeout", 10*time.Second, "how long to wait for the spawned binary to answer /rpc/_introspect")
	asJSON := fs.Bool("json", false, "dump the raw HealthReport JSON instead of pretty sections")
	watch := fs.Duration("watch", 0, "re-poll every <interval>; 0 disables (single shot). Ctrl-C exits.")
	var headers catalog.StringSliceFlag
	fs.Var(&headers, "header", "extra header on the health fetch, K=V; repeatable")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	src, cleanup, err := catalog.ResolveSource(*from, *execBin, *execTimeout, stderr)
	if err != nil {
		if errors.Is(err, catalog.ErrSourceUsage) {
			fmt.Fprintf(stderr, "sov health: %v\n", err)
			fs.Usage()
			return 2
		}
		fmt.Fprintf(stderr, "sov health: spawn %s: %v\n", *execBin, err)
		return 1
	}
	defer cleanup()

	if *watch <= 0 {
		return runOnce(src, headers, *asJSON, stdout, stderr)
	}
	return runWatch(src, headers, *asJSON, *watch, stdout, stderr)
}

func runOnce(from string, headers []string, asJSON bool, stdout, stderr io.Writer) int {
	report, err := fetchHealth(from, headers)
	if err != nil {
		fmt.Fprintf(stderr, "sov health: fetch %s: %v\n", from, err)
		return 1
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
	} else {
		emit(stdout, report)
	}
	// Map status → exit code so CI can gate. degraded counts as
	// non-zero because operators usually want to know.
	switch report.Status {
	case "healthy":
		return 0
	default:
		return 1
	}
}

func runWatch(from string, headers []string, asJSON bool, interval time.Duration, stdout, stderr io.Writer) int {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	tick := time.NewTicker(interval)
	defer tick.Stop()

	render := func() string {
		report, err := fetchHealth(from, headers)
		if err != nil {
			fmt.Fprintf(stderr, "sov health: fetch %s: %v\n", from, err)
			return "unreachable"
		}
		fmt.Fprintf(stdout, "[%s] overall: %s\n", time.Now().Format(time.RFC3339), colorStatus(report.Status))
		if asJSON {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(report)
		} else {
			emit(stdout, report)
		}
		return report.Status
	}
	last := render()
	for {
		select {
		case <-stop:
			fmt.Fprintln(stderr, "sov health: caught signal; exiting watch loop")
			if last == "healthy" {
				return 0
			}
			return 1
		case <-tick.C:
			last = render()
		}
	}
}

// fetchHealth POSTs /rpc/_health and decodes into HealthReport.
// Mirrors catalog.Fetch's header pass-through + 15s timeout pattern
// so the wire surface stays consistent across subcommands.
func fetchHealth(base string, headers []string) (*gateway.HealthReport, error) {
	url := strings.TrimRight(base, "/") + "/rpc/_health"
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
	// 200 healthy, 207 degraded, 503 unhealthy — all decode cleanly,
	// so we DON'T error on >=400. Only treat hard 4xx (404/405) as
	// failures: the endpoint must exist.
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil, fmt.Errorf("status %d: endpoint not available", resp.StatusCode)
	}
	var report gateway.HealthReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		return nil, fmt.Errorf("decode health: %w", err)
	}
	return &report, nil
}

func emit(w io.Writer, report *gateway.HealthReport) {
	output.Heading(w, "Health")
	fmt.Fprintf(w, "Overall:    %s\n", colorStatus(report.Status))
	fmt.Fprintf(w, "Gateway:    %s\n", colorStatus(report.Gateway.Status))
	fmt.Fprintf(w, "Checked at: %s\n\n", report.CheckedAt.Format(time.RFC3339))

	if len(report.Services) == 0 {
		fmt.Fprintln(w, "  (no services registered)")
		return
	}
	names := make([]string, 0, len(report.Services))
	for n := range report.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	rows := make([][]string, 0, len(names))
	for _, name := range names {
		s := report.Services[name]
		loc := "remote"
		if s.Local {
			loc = "local"
		}
		src := s.Source
		if src == "" && s.Local {
			src = "(in-process)"
		}
		detail := s.Detail
		if detail == "" && len(s.Children) > 0 {
			detail = fmt.Sprintf("%d child service(s)", len(s.Children))
		}
		rows = append(rows, []string{name, colorStatus(s.Status), loc, src, detail})
	}
	output.Table(w, []string{"SERVICE", "STATUS", "WHERE", "SOURCE", "DETAIL"}, rows)
}

// colorStatus returns the status string wrapped in the appropriate
// ANSI color: green healthy, yellow degraded/unknown, red unhealthy
// or missing. When stdout isn't a TTY the wrappers fall back to the
// raw string.
func colorStatus(s string) string {
	switch s {
	case "healthy":
		return output.Color(s, output.Green)
	case "degraded", "unknown":
		return output.Color(s, output.Yellow)
	case "unhealthy", "missing":
		return output.Color(s, output.Red)
	default:
		return s
	}
}
