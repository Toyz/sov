// Package output provides TTY-aware rendering helpers shared by the
// sov CLI subcommands that print to humans (drift, inspect, health).
//
// Zero deps — hand-rolled ANSI escape sequences gated on a stdout
// isatty check. When stdout is a pipe/file or NO_COLOR is set, all
// color helpers emit the raw text.
package output

import (
	"io"
	"os"
	"strings"
)

// ANSI color codes. Use the constants directly with Color so swapping
// schemes is a one-edit job.
const (
	Reset  = "\x1b[0m"
	Bold   = "\x1b[1m"
	Dim    = "\x1b[2m"
	Red    = "\x1b[31m"
	Green  = "\x1b[32m"
	Yellow = "\x1b[33m"
	Blue   = "\x1b[34m"
	Cyan   = "\x1b[36m"
	Gray   = "\x1b[90m"
)

// IsTTY reports whether stdout is connected to a terminal. Pipes and
// redirects return false, suppressing color. Respects NO_COLOR per
// https://no-color.org/.
func IsTTY() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// Color wraps s with ANSI code only when stdout is a TTY. When called
// against a non-TTY writer, returns s unmodified.
func Color(s, code string) string {
	if !IsTTY() {
		return s
	}
	return code + s + Reset
}

// Heading prints s as a section heading with a "===" underline. Title
// + underline render in cyan when on a TTY.
func Heading(w io.Writer, s string) {
	bar := strings.Repeat("=", len(s))
	io.WriteString(w, Color(s, Cyan)+"\n")
	io.WriteString(w, Color(bar, Cyan)+"\n")
}

// Table renders a simple left-aligned aligned table. headers may be
// empty for a borderless rows-only render. Columns are sized to the
// widest cell. Uses two-space gutter; no fancy borders so output stays
// grep-friendly.
func Table(w io.Writer, headers []string, rows [][]string) {
	if len(rows) == 0 && len(headers) == 0 {
		return
	}
	cols := len(headers)
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	widths := make([]int, cols)
	for i, h := range headers {
		if len(h) > widths[i] {
			widths[i] = len(h)
		}
	}
	for _, r := range rows {
		for i, c := range r {
			if len(c) > widths[i] {
				widths[i] = len(c)
			}
		}
	}
	if len(headers) > 0 {
		writeRow(w, headers, widths, Bold)
		under := make([]string, cols)
		for i := range under {
			under[i] = strings.Repeat("-", widths[i])
		}
		writeRow(w, under, widths, Gray)
	}
	for _, r := range rows {
		writeRow(w, r, widths, "")
	}
}

func writeRow(w io.Writer, cells []string, widths []int, color string) {
	parts := make([]string, len(widths))
	for i := range widths {
		cell := ""
		if i < len(cells) {
			cell = cells[i]
		}
		pad := widths[i] - len(cell)
		if pad < 0 {
			pad = 0
		}
		parts[i] = cell + strings.Repeat(" ", pad)
	}
	line := strings.Join(parts, "  ")
	if color != "" {
		line = Color(line, color)
	}
	io.WriteString(w, line+"\n")
}
