package initcmd

import (
	"flag"
	"fmt"
	"go/format"
	"io"
	"os"
	"sort"
	"strings"
)

// hookSpec describes one selectable plugin hook: the gateway interface it
// satisfies, the method declarations to stub, and any extra std imports
// the stubs need.
type hookSpec struct {
	iface   string   // gateway.<iface> for the var _ assertion
	methods []string // full method declarations (empty/zero-value bodies)
	imports []string // extra imports beyond "github.com/Toyz/sov/gateway"
}

// pluginHooks is the author-facing hook menu for `sov init plugin`. The
// framework-shape interfaces (Resolver, Server, Logger) are intentionally
// omitted — they replace core machinery, not extend it.
var pluginHooks = map[string]hookSpec{
	"ConfigApplier": {"ConfigApplier", []string{
		"func (p *Plugin) Apply(g *gateway.Gateway) error {\n\t// runs inside gw.Use, before any other hook — mutate gateway config here\n\treturn nil\n}"}, nil},
	"BootValidator": {"BootValidator", []string{
		"func (p *Plugin) ValidateBoot(g *gateway.Gateway) error {\n\t// return an error to refuse startup\n\treturn nil\n}"}, nil},
	"LifecycleHook": {"LifecycleHook", []string{
		"func (p *Plugin) OnStart(ctx context.Context) error { return nil }",
		"func (p *Plugin) OnStop(ctx context.Context) error  { return nil }"}, []string{"context"}},
	"HeaderInjector": {"HeaderInjector", []string{
		"func (p *Plugin) InjectHeaders(ctx context.Context, req *gateway.Request, hreq *http.Request) error {\n\t// add headers to the outbound proxy hop\n\treturn nil\n}"}, []string{"context", "net/http"}},
	"HeaderParser": {"HeaderParser", []string{
		"func (p *Plugin) ParseHeaders(req *gateway.Request) *rpc.Error {\n\t// read inbound headers; return non-nil to short-circuit dispatch\n\treturn nil\n}"}, []string{"github.com/Toyz/sov/rpc"}},
	"HeaderClaimer": {"HeaderClaimer", []string{
		"func (p *Plugin) ClaimedHeaders() []string {\n\t// header names that bypass the X-Sov-* identity strip\n\treturn nil\n}"}, nil},
	"AuthTranslator": {"AuthTranslator", []string{
		"func (p *Plugin) TranslateAuth(req *gateway.Request, claims *gateway.Claims) error {\n\t// claims may be nil (anonymous); translate identity into legacy headers\n\treturn nil\n}"}, nil},
	"DispatchHook": {"DispatchHook", []string{
		"func (p *Plugin) OnDispatch(ev gateway.DispatchEvent) error {\n\t// fires after every handler; offload slow work to your own goroutine\n\treturn nil\n}"}, nil},
	"ContextContributor": {"ContextContributor", []string{
		"func (p *Plugin) ContributeContext(ctx *rpc.Context, req *gateway.Request) error {\n\t// stash per-request metadata on the local-path ctx\n\treturn nil\n}"}, []string{"github.com/Toyz/sov/rpc"}},
	"ResponseInterceptor": {"ResponseInterceptor", []string{
		"func (p *Plugin) InterceptResponse(req *gateway.Request, resp *gateway.Response) error {\n\t// mutate or replace the resolved response\n\treturn nil\n}"}, nil},
	"Middlewarer": {"Middlewarer", []string{
		"func (p *Plugin) Wrap(next gateway.Handler) gateway.Handler {\n\treturn func(ctx context.Context, req *gateway.Request) *gateway.Response {\n\t\treturn next(ctx, req)\n\t}\n}"}, []string{"context"}},
	"IntrospectContributor": {"IntrospectContributor", []string{
		"func (p *Plugin) ContributeIntrospect(ctx context.Context, report *gateway.IntrospectReport, trace string, visited []string) error {\n\t// decorate the report or fan out to remotes (honor the visited guard)\n\treturn nil\n}"}, []string{"context"}},
	"HealthAggregator": {"HealthAggregator", []string{
		"func (p *Plugin) AggregateHealth(ctx context.Context, report *gateway.HealthReport) error {\n\t// merge remote-pod health into the local report\n\treturn nil\n}"}, []string{"context"}},
	"RouteHandler": {"RouteHandler", []string{
		"func (p *Plugin) RoutePatterns() []string {\n\treturn []string{\"/rpc/_myroute\"}\n}",
		"func (p *Plugin) ServeRoute(ctx context.Context, req *gateway.Request) *gateway.Response {\n\treturn &gateway.Response{Status: 200, Body: []byte(`{\"data\":\"ok\"}`)}\n}"}, []string{"context"}},
	"CapabilityProvider": {"CapabilityProvider", []string{
		"func (p *Plugin) Capabilities() []gateway.Capability {\n\treturn nil\n}"}, nil},
	"PluginDependency": {"PluginDependency", []string{
		"func (p *Plugin) Requires() []string { return nil } // gw.Use errors if a named plugin is absent",
		"func (p *Plugin) After() []string    { return nil } // soft ordering hint"}, nil},
}

func runPlugin(argv []string, stdout, stderr io.Writer) int {
	// Pull the leading positional <name> before flag parsing — Go's flag
	// package stops at the first non-flag arg, so `plugin <name> --hooks`
	// otherwise wouldn't see the flags. A leading flag (e.g. --list) is
	// fine: name stays empty.
	name := ""
	if len(argv) > 0 && !strings.HasPrefix(argv[0], "-") {
		name = argv[0]
		argv = argv[1:]
	}

	fs := flag.NewFlagSet("sov init plugin", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hooks := fs.String("hooks", "ConfigApplier", "comma-separated hooks to scaffold (see -list)")
	pkg := fs.String("pkg", "", "package name (default: derived from plugin name)")
	out := fs.String("out", "", "output file (default: stdout)")
	list := fs.Bool("list", false, "list available hooks and exit")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	if *list {
		printHookList(stdout)
		return 0
	}
	if name == "" || len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "usage: sov init plugin <name> [--hooks H1,H2] [--pkg p] [--out file.go] [--list]")
		return 2
	}

	selected, err := resolveHooks(*hooks)
	if err != nil {
		fmt.Fprintln(stderr, err)
		printHookList(stderr)
		return 2
	}

	pkgName := *pkg
	if pkgName == "" {
		pkgName = sanitizePkg(name)
	}
	src := renderPlugin(name, pkgName, selected)

	if *out == "" {
		fmt.Fprint(stdout, src)
		return 0
	}
	if err := os.WriteFile(*out, []byte(src), 0o644); err != nil {
		fmt.Fprintf(stderr, "sov init plugin: write %s: %v\n", *out, err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s (package %s, hooks: %s)\n", *out, pkgName, strings.Join(selected, ", "))
	return 0
}

// resolveHooks validates the requested hook names against the menu,
// returning them sorted for deterministic output.
func resolveHooks(csv string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, h := range strings.Split(csv, ",") {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if _, ok := pluginHooks[h]; !ok {
			return nil, fmt.Errorf("sov init plugin: unknown hook %q", h)
		}
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("sov init plugin: at least one hook required (a plugin with no hooks won't bind)")
	}
	sort.Strings(out)
	return out, nil
}

func renderPlugin(name, pkg string, hooks []string) string {
	// Collect extra imports across selected hooks.
	imps := map[string]bool{}
	var assertLines, methodBlocks []string
	for _, h := range hooks {
		spec := pluginHooks[h]
		for _, i := range spec.imports {
			imps[i] = true
		}
		assertLines = append(assertLines, fmt.Sprintf("\t_ gateway.%s = (*Plugin)(nil)", spec.iface))
		methodBlocks = append(methodBlocks, strings.Join(spec.methods, "\n"))
	}
	// Plugin + PluginDoc are always emitted (free metadata).
	assertLines = append([]string{
		"\t_ gateway.Plugin    = (*Plugin)(nil)",
		"\t_ gateway.PluginDoc = (*Plugin)(nil)",
	}, assertLines...)

	var b strings.Builder
	fmt.Fprintf(&b, "// Package %s is a Sov gateway plugin. Register it with gw.Use(%s.New(...)).\n", pkg, pkg)
	fmt.Fprintf(&b, "package %s\n\n", pkg)

	// imports: gateway always; extras sorted, std vs module grouped lightly.
	b.WriteString("import (\n")
	std, mod := splitImports(imps)
	for _, i := range std {
		fmt.Fprintf(&b, "\t%q\n", i)
	}
	if len(std) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("\t\"github.com/Toyz/sov/gateway\"\n")
	for _, i := range mod {
		fmt.Fprintf(&b, "\t%q\n", i)
	}
	b.WriteString(")\n\n")

	b.WriteString("// Config configures the plugin.\ntype Config struct{}\n\n")
	b.WriteString("// Plugin is the gateway plugin.\ntype Plugin struct{ cfg Config }\n\n")
	fmt.Fprintf(&b, "// New returns the plugin from cfg.\nfunc New(cfg Config) *Plugin { return &Plugin{cfg: cfg} }\n\n")

	b.WriteString("// Compile-time proof of the hooks this plugin binds — a signature\n")
	b.WriteString("// drift here is a build error, not a silent non-binding at runtime.\n")
	b.WriteString("var (\n")
	b.WriteString(strings.Join(assertLines, "\n"))
	b.WriteString("\n)\n\n")

	fmt.Fprintf(&b, "func (p *Plugin) PluginName() string { return %q }\n", name)
	fmt.Fprintf(&b, "func (p *Plugin) Doc() string        { return %q }\n\n", "TODO: describe "+name)
	b.WriteString(strings.Join(methodBlocks, "\n\n"))
	b.WriteString("\n")
	// Emit gofmt-clean source — align the var-assert block etc. Fall back
	// to raw on the (unexpected) parse error so we never emit nothing.
	if formatted, err := format.Source([]byte(b.String())); err == nil {
		return string(formatted)
	}
	return b.String()
}

func splitImports(imps map[string]bool) (std, mod []string) {
	for i := range imps {
		if strings.Contains(i, ".") {
			mod = append(mod, i)
		} else {
			std = append(std, i)
		}
	}
	sort.Strings(std)
	sort.Strings(mod)
	return std, mod
}

func sanitizePkg(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "myplugin"
	}
	return b.String()
}

func printHookList(w io.Writer) {
	names := make([]string, 0, len(pluginHooks))
	for n := range pluginHooks {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintln(w, "available hooks (see docs/PLUGIN_HOOKS.md):")
	for _, n := range names {
		fmt.Fprintf(w, "  %s\n", n)
	}
}
