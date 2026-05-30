package sov

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// LoadEnv reads each KEY=VALUE file at the supplied paths and calls
// os.Setenv for every entry. Missing files are skipped silently —
// useful for projects that ship .sov.env in dev but rely on real
// environment variables in production. Existing env vars are NOT
// overwritten (consistent with most .env conventions); call
// LoadEnvOverride when you want force.
//
//	func main() {
//	    sov.LoadEnv(".sov.env")
//	    gw := sov.NewMesh(...)
//	}
//
// Wire shape: one entry per line. Lines beginning with '#' or empty
// lines are skipped. VALUE may be quoted with single or double quotes
// (quotes are stripped); inline `#` after an unquoted value starts a
// comment. No shell-style $VAR interpolation — values are literal.
func LoadEnv(paths ...string) error {
	return loadEnvFiles(paths, false)
}

// LoadEnvOverride is LoadEnv with overwrite semantics — every parsed
// key replaces any existing env var. Use when the file is the source
// of truth and you want to reset stale values.
func LoadEnvOverride(paths ...string) error {
	return loadEnvFiles(paths, true)
}

func loadEnvFiles(paths []string, override bool) error {
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("sov.LoadEnv: open %s: %w", p, err)
		}
		err = parseEnvFile(f, override)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("sov.LoadEnv: %s: %w", p, err)
		}
	}
	return nil
}

func parseEnvFile(f *os.File, override bool) error {
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || raw[0] == '#' {
			continue
		}
		// Strip optional `export ` prefix so files sourced by bash
		// also parse here.
		raw = strings.TrimPrefix(raw, "export ")
		k, v, ok := strings.Cut(raw, "=")
		if !ok {
			return fmt.Errorf("line %d: expected KEY=VALUE, got %q", line, raw)
		}
		k = strings.TrimSpace(k)
		if k == "" {
			return fmt.Errorf("line %d: empty key", line)
		}
		v = parseValue(v)
		if !override {
			if _, present := os.LookupEnv(k); present {
				continue
			}
		}
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("line %d: setenv %s: %w", line, k, err)
		}
	}
	return sc.Err()
}

// parseValue strips wrapping quotes + trailing # comments on unquoted
// values. No shell-style interpolation.
//
// Quoting: a value that STARTS with a quote is taken up to the matching
// closing quote; the content between is returned verbatim and anything
// after (whitespace + an optional # comment) is ignored. This is why
// `KEY="secret" # note` yields `secret`, not `"secret"` — the closing
// quote, not the last byte of the line, terminates the value. A leading
// quote with no closing match falls through and is treated literally.
//
// Comments: on an UNQUOTED value, a `#` that follows whitespace (space or
// tab) starts a trailing comment. A `#` that is part of the token itself
// (no preceding whitespace — e.g. `#FF0000`, `0xDE#AD`, an API key with a
// fragment) is preserved.
func parseValue(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if q := v[0]; q == '"' || q == '\'' {
		if end := strings.IndexByte(v[1:], q); end >= 0 {
			return v[1 : 1+end]
		}
		// No closing quote — fall through and treat the value literally.
	}
	for i := 1; i < len(v); i++ {
		if v[i] == '#' && (v[i-1] == ' ' || v[i-1] == '\t') {
			v = v[:i]
			break
		}
	}
	return strings.TrimSpace(v)
}
