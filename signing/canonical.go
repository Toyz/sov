package signing

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// CanonicalMessage builds the byte string the client signed and the
// server must hash identically. Format:
//
//	v2\n<router>\n<method>\n<sha256_hex(body)>\n<unix_ts>\n
//
// Newlines are unambiguous separators. Body hash is hex-encoded so the
// string is ASCII-safe even when the request body contains binary.
func CanonicalMessage(router, method string, body []byte, unixTs int64) []byte {
	sum := sha256.Sum256(body)
	hexSum := hex.EncodeToString(sum[:])

	buf := make([]byte, 0, 4+len(router)+len(method)+len(hexSum)+20)
	buf = append(buf, "v2\n"...)
	buf = append(buf, router...)
	buf = append(buf, '\n')
	buf = append(buf, method...)
	buf = append(buf, '\n')
	buf = append(buf, hexSum...)
	buf = append(buf, '\n')
	buf = strconv.AppendInt(buf, unixTs, 10)
	buf = append(buf, '\n')
	return buf
}
