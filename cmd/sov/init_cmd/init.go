// Package initcmd implements `sov init <mode>` — the project
// scaffolder. Renders a small, canonical starting point for each
// deployment topology (monolith, hybrid, mesh, mesh tiered) plus a
// secrets-only mode that just writes a .sov.env.
//
// The package is named initcmd (not init) because `init` is a
// reserved Go identifier — the subcommand on the CLI is still
// `sov init <mode>`.
//
// Templates live under templates/<mode>/ and are baked in via
// go:embed so the binary is self-contained. Each template is a
// text/template with the variables documented on the Vars struct.
package initcmd

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"text/template"
)

// `all:` so dotfile templates like .gitignore.tmpl ship in the binary.
//
//go:embed all:templates
var templatesFS embed.FS

// Vars are the values exposed to every template. Kept small and
// stable so the templates stay readable.
type Vars struct {
	Project     string
	Module      string
	SecretsHMAC string
	SecretsMesh string
	// SovVersion is the github.com/Toyz/sov version stamped into a
	// freshly-written go.mod's require line — the version the running
	// `sov` binary was built against, so `go mod tidy` resolves instead
	// of choking on a v0.0.0 placeholder. "latest" when undeterminable.
	SovVersion string
}

// Run is the subcommand entry point. argv is the slice after
// "sov init" — i.e. argv[0] is the mode name (or "help"). Returns
// the process exit code so main can `os.Exit` directly.
func Run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 {
		usage(stderr)
		return 2
	}
	switch argv[0] {
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	case "secrets":
		return runSecrets(argv[1:], stdout, stderr)
	case "monolith", "hybrid":
		return runScaffold(argv[0], argv[1:], stdout, stderr)
	case "mesh":
		// "sov init mesh tiered ..." → tiered variant.
		if len(argv) >= 2 && argv[1] == "tiered" {
			return runScaffold("mesh_tiered", argv[2:], stdout, stderr)
		}
		return runScaffold("mesh", argv[1:], stdout, stderr)
	case "plugin":
		return runPlugin(argv[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "sov init: unknown mode %q\n", argv[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `sov init — scaffold a sov project

Usage:
  sov init <mode> [project-name] [flags]

Modes:
  monolith       single binary, all services in-process
  hybrid         single binary, mix of local + remote-registered services
  mesh           central gateway + N pods (docker-compose included)
  mesh tiered    prime gateway + team gateways + pods (federation demo)
  secrets        .sov.env with random HMAC + mesh secrets (no scaffold)
  plugin <name>  scaffold a gateway plugin (--hooks H1,H2 --out f.go --list)

Flags:
  -dir DIR       target directory (default ./<project-name>, or "." if no name)
  -module PATH   go module path (default example.com/<project-name>)
  -force         overwrite existing files
  -no-secrets    skip .sov.env on mesh modes
  -o PATH        (secrets mode) output file path (default .sov.env)`)
}

// runScaffold renders the named template tree into the target dir.
func runScaffold(mode string, argv []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("sov init "+modeLabel(mode), flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("dir", "", "target directory (default ./<project-name>)")
	module := flags.String("module", "", "go module path (default example.com/<project-name>)")
	force := flags.Bool("force", false, "overwrite existing files")
	noSecrets := flags.Bool("no-secrets", false, "skip .sov.env generation on mesh modes")
	// Stdlib flag stops at the first non-flag token, so accept the
	// project-name positional anywhere in argv by pulling it out first.
	projectName, flagArgs := extractPositional(argv)
	if err := flags.Parse(flagArgs); err != nil {
		return 2
	}
	if projectName == "" && flags.NArg() >= 1 {
		projectName = flags.Arg(0)
	}
	targetDir, project := resolveDir(*dir, projectName)
	if project == "" {
		fmt.Fprintln(stderr, "sov init: project name required (positional arg) or -dir must end in a non-empty directory name")
		return 2
	}
	mod := *module
	if mod == "" {
		mod = "example.com/" + project
	}

	// If the target sits inside an existing Go module, reuse it: don't
	// write a (nested, conflicting) go.mod — the scaffolded .go files
	// reference only github.com/Toyz/sov + sov example packages, never the
	// project's own module path, so they compile in the host module as-is.
	encMod, inModule := enclosingModule(targetDir)

	vars := Vars{Project: project, Module: mod, SovVersion: sovVersion()}

	// Mesh modes get a .sov.env baked alongside unless -no-secrets.
	wantSecrets := !*noSecrets && (mode == "mesh" || mode == "mesh_tiered")
	if wantSecrets {
		hmacSecret, err := randomHex(32)
		if err != nil {
			fmt.Fprintf(stderr, "sov init: generate hmac secret: %v\n", err)
			return 1
		}
		meshSecret, err := randomHex(32)
		if err != nil {
			fmt.Fprintf(stderr, "sov init: generate mesh secret: %v\n", err)
			return 1
		}
		vars.SecretsHMAC = hmacSecret
		vars.SecretsMesh = meshSecret
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "sov init: create %s: %v\n", targetDir, err)
		return 1
	}

	written, err := renderTree(mode, targetDir, vars, *force, inModule)
	if err != nil {
		fmt.Fprintf(stderr, "sov init: %v\n", err)
		return 1
	}

	if wantSecrets {
		envPath := filepath.Join(targetDir, ".sov.env")
		if err := writeEnvFile(envPath, vars.SecretsHMAC, vars.SecretsMesh, *force); err != nil {
			fmt.Fprintf(stderr, "sov init: %v\n", err)
			return 1
		}
		written = append(written, ".sov.env")
	}

	fmt.Fprintf(stdout, "sov init %s: wrote %d files into %s\n", modeLabel(mode), len(written), targetDir)
	for _, f := range written {
		fmt.Fprintf(stdout, "  %s\n", f)
	}
	if inModule {
		ver := vars.SovVersion
		if ver == "" {
			ver = "latest"
		}
		fmt.Fprintf(stdout, "\nReused existing module %q (no go.mod written).\nNext steps:\n  go get github.com/Toyz/sov@%s   # if not already required\n  go build ./...\n", encMod, ver)
	} else {
		fmt.Fprintf(stdout, "\nNext steps:\n  cd %s\n  go mod tidy\n", targetDir)
	}
	if mode == "mesh" || mode == "mesh_tiered" {
		fmt.Fprintln(stdout, "  set -a; . .sov.env; set +a")
		fmt.Fprintln(stdout, "  docker compose up --build")
	} else {
		fmt.Fprintln(stdout, "  go run .")
	}
	return 0
}

// runSecrets generates just a .sov.env (replaces the old `sov mesh init`).
func runSecrets(argv []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("sov init secrets", flag.ContinueOnError)
	flags.SetOutput(stderr)
	out := flags.String("o", ".sov.env", "output file path")
	force := flags.Bool("force", false, "overwrite if the output file already exists")
	if err := flags.Parse(argv); err != nil {
		return 2
	}

	hmacSecret, err := randomHex(32)
	if err != nil {
		fmt.Fprintf(stderr, "sov init secrets: generate hmac secret: %v\n", err)
		return 1
	}
	meshSecret, err := randomHex(32)
	if err != nil {
		fmt.Fprintf(stderr, "sov init secrets: generate mesh secret: %v\n", err)
		return 1
	}
	if err := writeEnvFile(*out, hmacSecret, meshSecret, *force); err != nil {
		fmt.Fprintf(stderr, "sov init secrets: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "sov init secrets: wrote %s (mode 0600)\n", *out)
	fmt.Fprintf(stdout, "  SOV_HMAC_SECRET=%s\n", hmacSecret)
	fmt.Fprintf(stdout, "  SOV_MESH_SECRET=%s\n", meshSecret)
	fmt.Fprintln(stdout, "Source with: 'set -a; . "+*out+"; set +a' (bash)")
	return 0
}

// modeLabel turns the internal mode key into the user-facing label.
func modeLabel(mode string) string {
	if mode == "mesh_tiered" {
		return "mesh tiered"
	}
	return mode
}

// resolveDir picks the target dir + project label from flags + args.
// If the positional project name looks like a path (contains a slash
// or starts with a dot), treat it as the target dir and derive the
// project label from its basename — that's the shape the docs
// recommend (`sov init monolith /tmp/foo` or `sov init mesh ./demo`).
func resolveDir(dir, projectName string) (string, string) {
	if projectName != "" && looksLikePath(projectName) {
		base := sanitizeProject(filepath.Base(filepath.Clean(projectName)))
		if dir == "" {
			dir = projectName
		}
		return dir, base
	}
	switch {
	case dir != "" && projectName != "":
		return dir, projectName
	case dir != "":
		return dir, sanitizeProject(filepath.Base(filepath.Clean(dir)))
	case projectName != "":
		return filepath.Join(".", projectName), projectName
	default:
		return ".", ""
	}
}

func looksLikePath(s string) bool {
	return strings.ContainsRune(s, filepath.Separator) ||
		strings.HasPrefix(s, ".") ||
		strings.HasPrefix(s, "/") ||
		strings.HasPrefix(s, "~")
}

// extractPositional pulls the first non-flag token out of argv so the
// CLI accepts `sov init mesh demo -dir foo` just as readily as
// `sov init mesh -dir foo demo`. Returns the lifted positional plus
// the remaining argv ready for flag.Parse. Knows which scaffold-mode
// flags take a value so the value token isn't mistaken for the
// positional.
func extractPositional(argv []string) (string, []string) {
	valueFlags := map[string]bool{"-dir": true, "--dir": true, "-module": true, "--module": true, "-o": true, "--o": true}
	rest := make([]string, 0, len(argv))
	var positional string
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		if strings.HasPrefix(tok, "-") {
			rest = append(rest, tok)
			// `-flag=value` is self-contained; `-flag value` consumes next token.
			if !strings.Contains(tok, "=") && valueFlags[tok] && i+1 < len(argv) {
				rest = append(rest, argv[i+1])
				i++
			}
			continue
		}
		if positional == "" {
			positional = tok
			continue
		}
		// Subsequent positionals stay in rest for flag.Parse to surface
		// via flags.Args() — keeps behavior debuggable.
		rest = append(rest, tok)
	}
	return positional, rest
}

// sanitizeProject collapses a directory base into something usable as
// a project label. We only return "" when there's nothing left.
func sanitizeProject(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "." || s == "/" {
		return ""
	}
	return s
}

// renderTree walks the embedded template dir for mode and writes
// every entry into dst with .tmpl stripped from the filename. Returns
// the list of relative file paths written so callers can report.
// sovModulePath is the import path of this framework module.
const sovModulePath = "github.com/Toyz/sov"

// enclosingModule walks up from dir looking for a go.mod, returning that
// module's declared path. dir need not exist yet — non-existent ancestors
// are skipped. Lets `sov init` detect "you're already in a module" and
// reuse it instead of writing a nested, conflicting go.mod.
func enclosingModule(dir string) (string, bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		raw, err := os.ReadFile(filepath.Join(abs, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				line = strings.TrimSpace(line)
				if rest, ok := strings.CutPrefix(line, "module "); ok {
					return strings.TrimSpace(rest), true
				}
			}
			return "", true // go.mod with no module line — still a module dir
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", false // reached filesystem root
		}
		abs = parent
	}
}

// sovVersion returns the github.com/Toyz/sov version the running `sov`
// binary was built against — a valid go.mod version literal for the
// require line. Returns "" when undeterminable (e.g. `go run` from the
// sov repo, where sov is the main module with a (devel) version); the
// go.mod template then OMITS the require entirely (an invalid literal
// like "latest" would make go.mod unparseable) and `go mod tidy` adds it.
func sovVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	clean := func(v string) string {
		if v == "" || v == "(devel)" {
			return ""
		}
		return v
	}
	if bi.Main.Path == sovModulePath {
		if v := clean(bi.Main.Version); v != "" {
			return v
		}
	}
	for _, d := range bi.Deps {
		if d.Path == sovModulePath {
			if v := clean(d.Version); v != "" {
				return v
			}
		}
	}
	return ""
}

func renderTree(mode, dst string, vars Vars, force, skipGoMod bool) ([]string, error) {
	root := filepath.ToSlash(filepath.Join("templates", mode))
	var written []string
	walkErr := fs.WalkDir(templatesFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(p, root+"/")
		// Strip trailing .tmpl from rendered filename.
		outName := strings.TrimSuffix(rel, ".tmpl")
		// Reuse the host module: don't drop a nested go.mod.
		if skipGoMod && outName == "go.mod" {
			return nil
		}
		outPath := filepath.Join(dst, filepath.FromSlash(outName))

		if !force {
			if _, statErr := os.Stat(outPath); statErr == nil {
				return fmt.Errorf("%s already exists; pass -force to overwrite", outPath)
			}
		}
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return err
		}

		raw, err := templatesFS.ReadFile(p)
		if err != nil {
			return err
		}
		tmpl, err := template.New(p).Option("missingkey=error").Parse(string(raw))
		if err != nil {
			return fmt.Errorf("parse %s: %w", p, err)
		}
		f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if err := tmpl.Execute(f, vars); err != nil {
			_ = f.Close()
			return fmt.Errorf("render %s: %w", p, err)
		}
		if err := f.Close(); err != nil {
			return err
		}
		written = append(written, outName)
		return nil
	})
	return written, walkErr
}

// writeEnvFile writes a .sov.env with the supplied secrets at mode 0600.
func writeEnvFile(path, hmacSecret, meshSecret string, force bool) error {
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("%s already exists; pass -force to overwrite", path)
	}
	body := fmt.Sprintf(`# Generated by 'sov init'. Adjust as you wish.
# Source this file from every gateway + pod in the mesh — the same
# values must appear on every side, otherwise registers fail with 401.
#
# Production deployments should source these from a real secret store
# (vault, cloud KMS, k8s secret) rather than committing them.
SOV_HMAC_SECRET=%s
SOV_MESH_SECRET=%s
`, hmacSecret, meshSecret)
	return os.WriteFile(path, []byte(body), 0o600)
}

// randomHex returns n random bytes as a hex string.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
