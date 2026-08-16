// Package inspect implements `sov inspect` — pretty-print of the
// /rpc/_introspect catalog. Sections: services + method counts, types
// + field counts, plugins + hooks + capabilities, plus a drift
// summary that links to `sov drift` for the divergence detail.
//
// Same --from / --exec / --header flag shape as `sov gen` so the
// invocation surface stays consistent across all introspect-consuming
// subcommands.
package inspect

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/Toyz/sov/cmd/sov/internal/catalog"
	"github.com/Toyz/sov/cmd/sov/internal/output"
	"github.com/Toyz/sov/gateway"
)

// Run executes the inspect subcommand.
func Run(argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sov inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "", "gateway base URL; CLI fetches {from}/rpc/_introspect. Mutually exclusive with --exec.")
	execBin := fs.String("exec", "", "path to a sov gateway binary; spawns it on a free local port, fetches, kills. Honors SOV_LISTEN.")
	execTimeout := fs.Duration("exec-timeout", 10*time.Second, "how long to wait for the spawned binary to answer /rpc/_introspect")
	asJSON := fs.Bool("json", false, "dump the raw IntrospectReport JSON instead of pretty sections")
	var headers catalog.StringSliceFlag
	fs.Var(&headers, "header", "extra header on the introspect fetch, K=V; repeatable")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	src, cleanup, err := catalog.ResolveSource(*from, *execBin, *execTimeout, stderr)
	if err != nil {
		if errors.Is(err, catalog.ErrSourceUsage) {
			fmt.Fprintf(stderr, "sov inspect: %v\n", err)
			fs.Usage()
			return 2
		}
		fmt.Fprintf(stderr, "sov inspect: spawn %s: %v\n", *execBin, err)
		return 1
	}
	defer cleanup()
	report, err := catalog.Fetch(src, headers)
	if err != nil {
		fmt.Fprintf(stderr, "sov inspect: fetch %s: %v\n", *from, err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return 0
	}
	emit(stdout, report)
	return 0
}

func emit(w io.Writer, report *gateway.IntrospectReport) {
	emitServices(w, report)
	fmt.Fprintln(w)
	emitTypes(w, report)
	fmt.Fprintln(w)
	emitBoundaries(w, report)
	fmt.Fprintln(w)
	emitPlugins(w, report)
	fmt.Fprintln(w)
	emitDrift(w, report)
}

func emitServices(w io.Writer, report *gateway.IntrospectReport) {
	output.Heading(w, "Services")
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
		routers := report.Services[name]
		methodCount := 0
		routerNames := make([]string, 0, len(routers))
		for _, r := range routers {
			methodCount += len(r.Methods)
			routerNames = append(routerNames, r.Router)
		}
		rows = append(rows, []string{
			name,
			fmt.Sprintf("%d", len(routers)),
			fmt.Sprintf("%d", methodCount),
		})
	}
	output.Table(w, []string{"SERVICE", "ROUTERS", "METHODS"}, rows)
}

func emitTypes(w io.Writer, report *gateway.IntrospectReport) {
	output.Heading(w, "Types")
	if len(report.Types) == 0 {
		fmt.Fprintln(w, "  (no types registered)")
		return
	}
	names := make([]string, 0, len(report.Types))
	for n := range report.Types {
		names = append(names, n)
	}
	sort.Strings(names)
	rows := make([][]string, 0, len(names))
	for _, name := range names {
		td := report.Types[name]
		owner := td.Owner
		switch {
		case len(td.Owners) > 1:
			// Ambiguous — list every producer so the OWNER column shows who.
			owner = strings.Join(td.Owners, ", ") + " (ambiguous)"
		case owner == "":
			owner = "—"
		}
		rows = append(rows, []string{
			name,
			fmt.Sprintf("%d", len(td.Fields)),
			fmt.Sprintf("%d", len(td.UsedBy)),
			owner,
			joinOr(td.Consumers, "—"),
		})
	}
	output.Table(w, []string{"TYPE", "FIELDS", "USED BY", "OWNER", "CONSUMERS"}, rows)
}

func emitBoundaries(w io.Writer, report *gateway.IntrospectReport) {
	output.Heading(w, "Boundaries")
	if len(report.BoundaryWarnings) == 0 {
		fmt.Fprintln(w, output.Color("  No data-ownership warnings — every type has a single producer.", output.Green))
		return
	}
	for _, warn := range report.BoundaryWarnings {
		fmt.Fprintf(w, "  %s %s\n", output.Color("!", output.Yellow), warn)
	}
}

func emitPlugins(w io.Writer, report *gateway.IntrospectReport) {
	output.Heading(w, "Plugins")
	if len(report.Plugins) == 0 {
		fmt.Fprintln(w, "  (no plugins registered, or federated probe — root catalog only)")
		return
	}
	rows := make([][]string, 0, len(report.Plugins))
	for _, p := range report.Plugins {
		hooks := joinOr(p.Hooks, "(none)")
		caps := joinOr(p.Capabilities, "—")
		rows = append(rows, []string{p.Name, hooks, caps})
	}
	output.Table(w, []string{"NAME", "HOOKS", "CAPABILITIES"}, rows)
	// Print plugin docs (Extra["doc"]) under the table when present.
	docs := [][2]string{}
	for _, p := range report.Plugins {
		if p.Extra == nil {
			continue
		}
		if d, ok := p.Extra["doc"].(string); ok && d != "" {
			docs = append(docs, [2]string{p.Name, d})
		}
	}
	if len(docs) > 0 {
		fmt.Fprintln(w)
		for _, d := range docs {
			fmt.Fprintf(w, "  %s — %s\n", output.Color(d[0], output.Bold), d[1])
		}
	}
}

func emitDrift(w io.Writer, report *gateway.IntrospectReport) {
	output.Heading(w, "Drift")
	n := len(report.CrossRefs)
	if n == 0 {
		fmt.Fprintln(w, output.Color("  No type-shape drift detected.", output.Green))
		return
	}
	fmt.Fprintf(w, "  %s %d type name(s) diverge across the mesh. Run `sov drift -from <url>` for detail.\n",
		output.Color("!", output.Yellow), n)
}

func joinOr(items []string, fallback string) string {
	if len(items) == 0 {
		return fallback
	}
	sort.Strings(items)
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
