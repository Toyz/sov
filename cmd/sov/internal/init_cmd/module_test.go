package initcmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnclosingModule(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/host\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A deep, not-yet-existing subdir still resolves to the ancestor module.
	sub := filepath.Join(root, "a", "b", "c")
	if p, ok := enclosingModule(sub); !ok || p != "example.com/host" {
		t.Errorf("enclosingModule(sub) = %q,%v; want example.com/host,true", p, ok)
	}
	// A tempdir with no go.mod ancestor (under the OS temp root) → none.
	if p, ok := enclosingModule(t.TempDir()); ok {
		t.Errorf("enclosingModule(isolated) = %q,true; want \"\",false", p)
	}
}

func TestScaffold_ReusesEnclosingModule(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/host\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := Run([]string{"monolith", "demo", "--dir", filepath.Join(root, "svc")}, &out, &errb); code != 0 {
		t.Fatalf("Run rc=%d, err=%s", code, errb.String())
	}
	// No nested go.mod — the host module is reused.
	if _, err := os.Stat(filepath.Join(root, "svc", "go.mod")); err == nil {
		t.Error("nested go.mod written into an existing module; should be skipped")
	}
	// But the .go file IS written.
	if _, err := os.Stat(filepath.Join(root, "svc", "main.go")); err != nil {
		t.Errorf("main.go not written: %v", err)
	}
	if !strings.Contains(out.String(), "Reused existing module") {
		t.Errorf("missing reuse message:\n%s", out.String())
	}
}

func TestScaffold_WritesGoModWhenNoModule(t *testing.T) {
	// A target with no module ancestor gets a fresh, VALID go.mod (module
	// + go directive; require omitted here since the test binary's sov is
	// (devel) — `go mod tidy` adds it).
	dir := filepath.Join(t.TempDir(), "proj")
	var out, errb bytes.Buffer
	if code := Run([]string{"monolith", "demo", "--dir", dir}, &out, &errb); code != 0 {
		t.Fatalf("Run rc=%d, err=%s", code, errb.String())
	}
	raw, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatalf("go.mod not written: %v", err)
	}
	if !strings.Contains(string(raw), "module example.com/demo") {
		t.Errorf("go.mod missing module line:\n%s", raw)
	}
	// Never the invalid `latest` literal in a require.
	if strings.Contains(string(raw), "require github.com/Toyz/sov latest") {
		t.Errorf("go.mod has invalid 'latest' version literal:\n%s", raw)
	}
}
