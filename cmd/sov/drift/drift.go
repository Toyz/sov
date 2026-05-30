// Package drift implements `sov drift` — the drift-check subcommand.
//
// Folds what the legacy gateway/builtin/drift plugin used to do (poll
// /rpc/_introspect, log type-shape divergence) into an operator-side
// CLI. Drift is a build/release-time concern: the same gateway binary
// shouldn't have to carry a sidecar just to surface what `sov drift`
// in CI can detect on every PR.
//
// Wire conventions (same as `sov gen`):
//   - --from <url>  hit a running gateway directly
//   - --exec <bin>  spawn the binary on a free local port, fetch, kill
//   - --header K=V  repeatable; passed through on the introspect POST
//
// Exit codes:
//   - 0 when CrossRefs is empty (no drift)
//   - 1 when ANY drift entry exists (CI gate)
//   - 2 on flag/arg errors
package drift

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Toyz/sov/cmd/sov/internal/catalog"
	"github.com/Toyz/sov/cmd/sov/internal/output"
	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

// Run executes the drift subcommand.
func Run(argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sov drift", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "", "gateway base URL; CLI fetches {from}/rpc/_introspect. Mutually exclusive with --exec.")
	execBin := fs.String("exec", "", "path to a sov gateway binary; spawns it on a free local port, fetches, kills. Honors SOV_LISTEN.")
	execTimeout := fs.Duration("exec-timeout", 10*time.Second, "how long to wait for the spawned binary to answer /rpc/_introspect")
	asJSON := fs.Bool("json", false, "emit JSON instead of the pretty-printed table")
	watch := fs.Duration("watch", 0, "re-poll every <interval>; 0 disables (single shot). Ctrl-C exits.")
	var headers catalog.StringSliceFlag
	fs.Var(&headers, "header", "extra header on the introspect fetch, K=V; repeatable")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	src, cleanup, err := catalog.ResolveSource(*from, *execBin, *execTimeout, stderr)
	if err != nil {
		if errors.Is(err, catalog.ErrSourceUsage) {
			fmt.Fprintf(stderr, "sov drift: %v\n", err)
			fs.Usage()
			return 2
		}
		fmt.Fprintf(stderr, "sov drift: spawn %s: %v\n", *execBin, err)
		return 1
	}
	defer cleanup()

	if *watch <= 0 {
		return runOnce(src, headers, *asJSON, stdout, stderr)
	}
	return runWatch(src, headers, *asJSON, *watch, stdout, stderr)
}

// runOnce performs a single probe + render. Returns 1 on drift, 0
// clean, 1 on fetch failure.
func runOnce(from string, headers []string, asJSON bool, stdout, stderr io.Writer) int {
	report, err := catalog.Fetch(from, headers)
	if err != nil {
		fmt.Fprintf(stderr, "sov drift: fetch %s: %v\n", from, err)
		return 1
	}
	if asJSON {
		emitJSON(stdout, report)
	} else {
		emitPretty(stdout, report)
	}
	if len(report.CrossRefs) == 0 {
		return 0
	}
	return 1
}

// runWatch loops until SIGINT/SIGTERM. Cumulative drift count is
// tracked across polls (newly-detected names since process start).
// Exit code reflects whether ANY drift was ever observed.
func runWatch(from string, headers []string, asJSON bool, interval time.Duration, stdout, stderr io.Writer) int {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	seen := map[string]struct{}{}
	tick := time.NewTicker(interval)
	defer tick.Stop()

	// fire once immediately so operators don't wait `interval` before
	// the first render
	render := func() {
		report, err := catalog.Fetch(from, headers)
		if err != nil {
			fmt.Fprintf(stderr, "sov drift: fetch %s: %v\n", from, err)
			return
		}
		for name := range report.CrossRefs {
			seen[name] = struct{}{}
		}
		fmt.Fprintf(stdout, "[%s] cumulative drift names: %d (current: %d)\n",
			time.Now().Format(time.RFC3339), len(seen), len(report.CrossRefs))
		if asJSON {
			emitJSON(stdout, report)
		} else {
			emitPretty(stdout, report)
		}
	}
	render()
	for {
		select {
		case <-stop:
			fmt.Fprintln(stderr, "sov drift: caught signal; exiting watch loop")
			if len(seen) > 0 {
				return 1
			}
			return 0
		case <-tick.C:
			render()
		}
	}
}

// emitPretty renders one block per drift entry. Block layout matches
// the spec in the restructure brief:
//
//	DRIFT: User
//	  variant 1 (3 services) — hash abc12345 — services: chirp, feed
//	    fields: id:string, handle:string, display:string
//	  variant 2 (1 service)  — hash def67890 — services: registry
//	    fields: id:string, handle:string  ← MISSING display
//
//	3 drift entries across 4 services. Exit 1.
func emitPretty(w io.Writer, report *gateway.IntrospectReport) {
	if len(report.CrossRefs) == 0 {
		fmt.Fprintln(w, output.Color("No drift detected.", output.Green))
		fmt.Fprintf(w, "Catalog: %d service(s), %d type(s).\n", serviceCount(report), len(report.Types))
		return
	}

	names := make([]string, 0, len(report.CrossRefs))
	for n := range report.CrossRefs {
		names = append(names, n)
	}
	sort.Strings(names)

	totalSvc := map[string]struct{}{}
	for _, name := range names {
		entry := report.CrossRefs[name]
		fmt.Fprintf(w, "%s %s\n", output.Color("DRIFT:", output.Red), output.Color(name, output.Bold))
		// canonical = the variant with the most fields; missing-field
		// markers are computed against it so the divergence stands out
		canonical := pickCanonical(entry.Variants)
		canonicalFields := fieldNameSet(canonical.Fields)
		for i, v := range entry.Variants {
			svcs := append([]string(nil), v.Services...)
			sort.Strings(svcs)
			for _, s := range svcs {
				totalSvc[s] = struct{}{}
			}
			plural := "services"
			if len(svcs) == 1 {
				plural = "service"
			}
			hash := v.ShapeHash
			if len(hash) > 8 {
				hash = hash[:8]
			}
			fmt.Fprintf(w, "  variant %d (%d %s) — hash %s — services: %s\n",
				i+1, len(svcs), plural, hash, strings.Join(svcs, ", "))
			fmt.Fprintf(w, "    fields: %s\n", renderFields(v.Fields, canonicalFields))
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "%s %d drift %s across %d %s.\n",
		output.Color("==>", output.Yellow),
		len(names), plural("entry", "entries", len(names)),
		len(totalSvc), plural("service", "services", len(totalSvc)))
}

// emitJSON dumps the raw CrossRefs map. Operators piping into jq get
// the unmodified gateway shape (TypeVariants), which is exactly what
// CI scripts already parse.
func emitJSON(w io.Writer, report *gateway.IntrospectReport) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{
		"drift_count": len(report.CrossRefs),
		"cross_refs":  report.CrossRefs,
	})
}

func pickCanonical(variants []gateway.TypeVariant) gateway.TypeVariant {
	if len(variants) == 0 {
		return gateway.TypeVariant{}
	}
	best := variants[0]
	for _, v := range variants[1:] {
		if len(v.Fields) > len(best.Fields) {
			best = v
		}
	}
	return best
}

func fieldNameSet(fields []rpc.ParamField) map[string]struct{} {
	out := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		out[f.JSONName] = struct{}{}
	}
	return out
}

// renderFields prints "name:type, name:type, ..." with a trailing
// "← MISSING x, y" marker for any names present in canonical but
// absent in fields.
func renderFields(fields []rpc.ParamField, canonical map[string]struct{}) string {
	parts := make([]string, 0, len(fields))
	have := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		t := f.SchemaType
		if t == "" {
			t = "any"
		}
		parts = append(parts, f.JSONName+":"+t)
		have[f.JSONName] = struct{}{}
	}
	line := strings.Join(parts, ", ")
	missing := []string{}
	for name := range canonical {
		if _, ok := have[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		line += "  " + output.Color("← MISSING "+strings.Join(missing, ", "), output.Yellow)
	}
	return line
}

func serviceCount(report *gateway.IntrospectReport) int { return len(report.Services) }

func plural(one, many string, n int) string {
	if n == 1 {
		return one
	}
	return many
}
