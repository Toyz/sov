package sov_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Toyz/sov"
)

func writeEnv(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, ".sov.env")
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func TestLoadEnv_BasicAndComments(t *testing.T) {
	path := writeEnv(t, `# comment
KEY1=value1
# another
KEY2 = value2
export KEY3=value3
`)
	t.Setenv("KEY1", "")
	t.Setenv("KEY2", "")
	t.Setenv("KEY3", "")
	os.Unsetenv("KEY1")
	os.Unsetenv("KEY2")
	os.Unsetenv("KEY3")

	if err := sov.LoadEnv(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("KEY1"); got != "value1" {
		t.Errorf("KEY1=%q", got)
	}
	if got := os.Getenv("KEY2"); got != "value2" {
		t.Errorf("KEY2=%q", got)
	}
	if got := os.Getenv("KEY3"); got != "value3" {
		t.Errorf("KEY3=%q", got)
	}
}

func TestLoadEnv_QuotedValues(t *testing.T) {
	path := writeEnv(t, `Q1="hello world"
Q2='single quoted'
Q3=bare # trailing comment
Q4="value with #hash inside"
`)
	for _, k := range []string{"Q1", "Q2", "Q3", "Q4"} {
		os.Unsetenv(k)
	}
	if err := sov.LoadEnv(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("Q1"); got != "hello world" {
		t.Errorf("Q1=%q", got)
	}
	if got := os.Getenv("Q2"); got != "single quoted" {
		t.Errorf("Q2=%q", got)
	}
	if got := os.Getenv("Q3"); got != "bare" {
		t.Errorf("Q3=%q", got)
	}
	if got := os.Getenv("Q4"); got != "value with #hash inside" {
		t.Errorf("Q4=%q", got)
	}
}

func TestLoadEnv_ValueEdgeCases(t *testing.T) {
	// Regression cases: quoted values followed by a comment must still be
	// unquoted; tab-delimited comments must be stripped; tight tokens and
	// hex/# fragments must survive untouched.
	path := writeEnv(t, "QC=\"secret\" # prod key\n"+
		"SC='p@ss word' # the password\n"+
		"TAB=tok\t# tab comment\n"+
		"HEX=#FF0000\n"+
		"FRAG=0xDE#AD\n"+
		"TOK=eyJabc.def#ghi\n"+
		"EMPTY=\"\"\n"+
		"INNER=\"a # b\" # trailing\n")
	want := map[string]string{
		"QC":    "secret",
		"SC":    "p@ss word",
		"TAB":   "tok",
		"HEX":   "#FF0000",
		"FRAG":  "0xDE#AD",
		"TOK":   "eyJabc.def#ghi",
		"EMPTY": "",
		"INNER": "a # b",
	}
	for k := range want {
		os.Unsetenv(k)
	}
	if err := sov.LoadEnv(path); err != nil {
		t.Fatal(err)
	}
	for k, w := range want {
		if got := os.Getenv(k); got != w {
			t.Errorf("%s=%q, want %q", k, got, w)
		}
	}
}

func TestLoadEnv_DoesNotOverwrite(t *testing.T) {
	path := writeEnv(t, `ALREADY=fromfile`)
	t.Setenv("ALREADY", "preset")
	if err := sov.LoadEnv(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("ALREADY"); got != "preset" {
		t.Errorf("ALREADY=%q want preset (no overwrite)", got)
	}
}

func TestLoadEnvOverride(t *testing.T) {
	path := writeEnv(t, `ALREADY=fromfile`)
	t.Setenv("ALREADY", "preset")
	if err := sov.LoadEnvOverride(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("ALREADY"); got != "fromfile" {
		t.Errorf("ALREADY=%q want fromfile (override)", got)
	}
}

func TestLoadEnv_MissingFileSkips(t *testing.T) {
	if err := sov.LoadEnv("/nonexistent/path/.sov.env"); err != nil {
		t.Errorf("missing file should not error: %v", err)
	}
}

func TestLoadEnv_BadLineErrors(t *testing.T) {
	path := writeEnv(t, "no_equals_sign")
	if err := sov.LoadEnv(path); err == nil {
		t.Fatal("expected parse error")
	}
}
