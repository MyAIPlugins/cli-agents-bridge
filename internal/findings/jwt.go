package findings

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
)

// A JWT is recognised by its structure, not by how one serialization of its
// header happens to look in base64. The first version looked for the base64 of
// `{"` (the letters eyJ) at the start of the header and of the payload; but the
// JSON a library emits is free to start with a space or a newline, and a claims
// set can be `{}` (e30), so valid tokens went through. RFC 7519 section 7.1 does
// not canonicalize the JSON before it is encoded.
//
// The rule is: three or more dot-separated base64url segments (a compact JWS, a
// compact JWE has five), where the first decodes to a JSON object. That is what
// a JWT header is, and nothing else needs to be true of the token to make it a
// credential.
const (
	// jwtMaxHeaderChars is the longest header looked at. A column is at most 1000
	// bytes, so a longer segment is already refused for its length; the cap keeps
	// the work per segment bounded whatever the caller sends.
	jwtMaxHeaderChars = 8 << 10

	// jwtMaxStarts is how many places in a segment a header may start at: the
	// start of the segment and the places right after a `_` or a `-`, which are
	// part of the base64url alphabet and so cannot tell a prefix (`token_`) from
	// the token. Bounded so that a segment made of separators costs a fixed amount.
	jwtMaxStarts = 16
)

// jwtCandidate finds the runs that could be a token: base64url characters and
// at least two dots. containsJWT decides.
var jwtCandidate = regexp.MustCompile(`[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]*){2,}`)

// containsJWT reports whether a dotted run holds a JWT: some segment decodes to
// a JSON object and is followed by a non-empty segment and by one more (empty is
// allowed for the signature of an unsecured token). The segment can sit after
// other dotted segments (`a.b.<token>`), which is why every position is tried.
func containsJWT(run string) bool {
	segments := strings.Split(run, ".")
	for i := 0; i+2 < len(segments); i++ {
		if segments[i+1] == "" {
			continue
		}
		if headerDecodesToObject(segments[i]) {
			return true
		}
	}
	return false
}

func headerDecodesToObject(segment string) bool {
	starts := 0
	for i := 0; i < len(segment) && starts < jwtMaxStarts; i++ {
		if i > 0 && segment[i-1] != '_' && segment[i-1] != '-' {
			continue
		}
		starts++
		if len(segment)-i > jwtMaxHeaderChars {
			continue
		}
		if decodesToJSONObject(segment[i:]) {
			return true
		}
	}
	return false
}

// decodesToJSONObject reports whether s is the unpadded base64url of one JSON
// object. json.Valid accepts surrounding white space, which is the point; that
// the first significant byte is `{` is what makes the valid value an object.
func decodesToJSONObject(s string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return false
	}
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(raw)
}
