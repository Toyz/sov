// Package docgen renders the shared title/desc/doc/example/deprecated
// doc-comment block that every sovgen emitter attaches to generated
// types and fields. The branching (how title+desc combine, the blank
// separator before the free-form doc body, the example/deprecated
// tags) is identical across languages; only the comment delimiters
// differ. Each emitter supplies a DocStyle so the OUTPUT BYTES stay
// byte-identical to the old per-emitter copies.
package docgen

import (
	"fmt"
	"io"
	"strings"

	"github.com/Toyz/sov/rpc"
)

// DocStyle captures the per-language comment delimiters and tag shapes.
//
//	TS:     {Opener: "/**", Closer: " */", LinePrefix: " * ",
//	         BlankPrefix: " *", BlankSeparator: true,
//	         ExampleTag: "@example ", DeprecatedLine: " * @deprecated"}
//	Kotlin: same as TS but ExampleTag "@sample ".
//	Swift:  {LinePrefix: "/// ", BlankSeparator: false,
//	         ExampleTag: "Example: ", DeprecatedLine: "@available(*, deprecated)"}
type DocStyle struct {
	// Opener / Closer wrap the block (e.g. "/**" / " */"). Empty for
	// per-line comment styles like Swift's "///".
	Opener string
	Closer string
	// LinePrefix is prepended (after indent) to every content line,
	// e.g. " * " or "/// ".
	LinePrefix string
	// BlankPrefix is the prefix for the blank separator line between
	// the header (title/desc) and the free-form doc body, e.g. " *".
	BlankPrefix string
	// BlankSeparator inserts BlankPrefix between header and doc body.
	BlankSeparator bool
	// ExampleTag prefixes the example line's content (after LinePrefix),
	// e.g. "@example ", "@sample ", "Example: ".
	ExampleTag string
	// DeprecatedLine is the full deprecated marker line (after indent,
	// WITHOUT LinePrefix — it is written verbatim), e.g. " * @deprecated"
	// or "@available(*, deprecated)".
	DeprecatedLine string
}

// Render writes a doc-comment block for the given title/desc/doc/
// example/deprecated values using style. When every value is empty and
// deprecated is false, it writes nothing. indent is prepended to every
// emitted line.
func Render(w io.Writer, indent string, style DocStyle, title, desc, doc, example string, deprecated bool) {
	if title == "" && desc == "" && doc == "" && example == "" && !deprecated {
		return
	}
	line := func(s string) { fmt.Fprintf(w, "%s%s%s\n", indent, style.LinePrefix, s) }

	if style.Opener != "" {
		fmt.Fprintf(w, "%s%s\n", indent, style.Opener)
	}
	if title != "" {
		if desc != "" {
			line(fmt.Sprintf("%s — %s", title, desc))
		} else {
			line(title)
		}
	} else if desc != "" {
		line(desc)
	}
	if doc != "" {
		if style.BlankSeparator && (title != "" || desc != "") {
			fmt.Fprintf(w, "%s%s\n", indent, style.BlankPrefix)
		}
		for _, l := range strings.Split(doc, "\n") {
			line(l)
		}
	}
	if example != "" {
		line(style.ExampleTag + example)
	}
	if deprecated {
		fmt.Fprintf(w, "%s%s\n", indent, style.DeprecatedLine)
	}
	if style.Closer != "" {
		fmt.Fprintf(w, "%s%s\n", indent, style.Closer)
	}
}

// RenderField is the ParamField convenience used for field docs.
func RenderField(w io.Writer, indent string, style DocStyle, f rpc.ParamField) {
	Render(w, indent, style, f.Title, f.Desc, f.Doc, f.Example, f.Deprecated)
}
