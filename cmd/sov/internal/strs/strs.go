// Package strs holds the tiny string helpers the code emitters share.
// Each gen/<lang> package previously carried its own byte-for-byte copy
// of Capitalize/IsIdent; this is the single source of truth.
package strs

import "unicode"

// Capitalize upper-cases the first rune of s. Empty string is returned
// unchanged. Used to build PascalCase type aliases (Router+Method+Result).
func Capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// IsIdent reports whether s is a legal identifier for codegen purposes:
// a non-empty string whose first rune is a letter or '_', and whose
// remaining runes are letters, digits, '_' or '.' (the dot allows
// dotted wire names to pass the gate).
func IsIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 && !(unicode.IsLetter(r) || r == '_') {
			return false
		}
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.') {
			return false
		}
	}
	return true
}
