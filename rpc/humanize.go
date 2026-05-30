package rpc

import (
	"strings"
	"unicode"
)

// RouterTitle turns a router wire name into a product-facing label:
// "Workspace" → "Workspace", "TicketKey" → "Ticket Key".
func RouterTitle(name string) string {
	return splitCamel(name)
}

// OperationTitle turns a Go method name into a product-facing label:
// "ListInvoices" → "List invoices".
func OperationTitle(name string) string {
	s := splitCamel(name)
	if s == "" {
		return s
	}
	// Sentence case: keep first word capitalized, lowercase the rest unless
	// they look like initialisms (all caps).
	parts := strings.Split(s, " ")
	for i, p := range parts {
		if i == 0 {
			continue
		}
		if isAllUpper(p) {
			continue
		}
		parts[i] = strings.ToLower(p)
	}
	return strings.Join(parts, " ")
}

func splitCamel(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	runes := []rune(s)
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			next := rune(0)
			if i+1 < len(runes) {
				next = runes[i+1]
			}
			if unicode.IsLower(prev) || (unicode.IsUpper(prev) && unicode.IsLower(next)) {
				b.WriteByte(' ')
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isAllUpper(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsUpper(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
