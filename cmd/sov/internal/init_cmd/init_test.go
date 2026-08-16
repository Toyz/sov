// Test the scaffolder by rendering each mode into a t.TempDir(), then
// — for the Go modes — rewriting go.mod with a replace directive that
// points at the local sov repo and running `go build ./...` inside.
// The build is the actual contract: as the framework API drifts, the
// templates must keep compiling against it. Failing the test is the
// fastest signal that a template needs updating.
package initcmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot returns the absolute path of the sov repo root. The test
// file lives at cmd/sov/init_cmd/init_test.go so the root is three
// dirs up from runtime.Caller's file.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// .../sov/cmd/sov/init_cmd/init_test.go → up 4 dirs
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

// scaffoldModes are the build-and-compile-test modes.
func TestScaffoldBuildsForEveryMode(t *testing.T) {
	cases := []struct {
		name      string
		argv      []string
		wantFiles []string
		buildDirs []string // dirs to invoke `go build` in (relative to scaffold dir)
	}{
		{
			name:      "monolith",
			argv:      []string{"monolith", "demo"},
			wantFiles: []string{"main.go", "go.mod", ".gitignore", "Makefile"},
			buildDirs: []string{"."},
		},
		{
			name:      "hybrid",
			argv:      []string{"hybrid", "demo"},
			wantFiles: []string{"main.go", "go.mod", ".gitignore", "Makefile"},
			buildDirs: []string{"."},
		},
		{
			name: "mesh",
			argv: []string{"mesh", "demo"},
			wantFiles: []string{
				"gateway/main.go", "pod/main.go", "Dockerfile",
				"docker-compose.yml", "go.mod", ".gitignore", "README.md",
				".sov.env",
			},
			buildDirs: []string{"./gateway", "./pod"},
		},
		{
			name: "mesh-tiered",
			argv: []string{"mesh", "tiered", "demo"},
			wantFiles: []string{
				"prime/main.go", "team/main.go", "pod/main.go",
				"Dockerfile", "docker-compose.yml", "go.mod",
				".gitignore", "README.md", ".sov.env",
			},
			buildDirs: []string{"./prime", "./team", "./pod"},
		},
	}

	root := repoRoot(t)
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			argv := append([]string{}, tc.argv...)
			argv = append(argv, "-dir", dir, "-force")
			var stdout, stderr bytes.Buffer
			if code := Run(argv, &stdout, &stderr); code != 0 {
				t.Fatalf("Run %v rc=%d\nstdout:%s\nstderr:%s", argv, code, stdout.String(), stderr.String())
			}
			for _, rel := range tc.wantFiles {
				if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
					t.Errorf("expected %s: %v", rel, err)
				}
			}
			rewriteModuleReplace(t, filepath.Join(dir, "go.mod"), root)
			for _, sub := range tc.buildDirs {
				runGo(t, dir, "mod", "tidy")
				runGoIn(t, filepath.Join(dir, sub), "build", "./...")
			}
		})
	}
}

func TestSecretsMode(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, ".sov.env")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"secrets", "-o", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("rc=%d stderr=%s", code, stderr.String())
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm=%o want 0600", info.Mode().Perm())
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.Contains(got, "SOV_HMAC_SECRET=") {
		t.Errorf("missing SOV_HMAC_SECRET line:\n%s", got)
	}
	if !strings.Contains(got, "SOV_MESH_SECRET=") {
		t.Errorf("missing SOV_MESH_SECRET line:\n%s", got)
	}
	// Sanity: secrets are 64 hex chars (32 bytes hex-encoded).
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "SOV_") {
			continue
		}
		_, val, _ := strings.Cut(line, "=")
		if len(val) != 64 {
			t.Errorf("expected 64-char hex secret on %q, got %d chars", line, len(val))
		}
	}

	// Re-running without -force must fail to avoid clobbering.
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"secrets", "-o", out}, &stdout, &stderr); code == 0 {
		t.Errorf("expected non-zero rc when .sov.env already exists")
	}

	// With -force it should overwrite cleanly.
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"secrets", "-o", out, "-force"}, &stdout, &stderr); code != 0 {
		t.Errorf("rc=%d with -force, stderr=%s", code, stderr.String())
	}
}

func TestSecretsScaffoldedAlongsideMesh(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"mesh", "demo", "-dir", dir, "-force"}, &stdout, &stderr); code != 0 {
		t.Fatalf("rc=%d stderr=%s", code, stderr.String())
	}
	envPath := filepath.Join(dir, ".sov.env")
	if _, err := os.Stat(envPath); err != nil {
		t.Fatalf("expected .sov.env in mesh scaffold: %v", err)
	}
}

func TestNoSecretsFlagSkipsEnvFile(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"mesh", "demo", "-dir", dir, "-force", "-no-secrets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("rc=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".sov.env")); !os.IsNotExist(err) {
		t.Errorf("expected no .sov.env with -no-secrets, got err=%v", err)
	}
}

func TestUnknownMode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"bogus"}, &stdout, &stderr); code == 0 {
		t.Errorf("expected non-zero rc on unknown mode")
	}
}

// rewriteModuleReplace appends a `replace github.com/Toyz/sov => <root>`
// directive to the scaffolded go.mod so the build test uses the
// in-repo framework source rather than trying to resolve v0.0.0.
func rewriteModuleReplace(t *testing.T, goMod, root string) {
	t.Helper()
	body, err := os.ReadFile(goMod)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, []byte("\nreplace github.com/Toyz/sov => "+root+"\n")...)
	if err := os.WriteFile(goMod, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGo(t *testing.T, dir string, args ...string) {
	t.Helper()
	runGoIn(t, dir, args...)
}

func runGoIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}
