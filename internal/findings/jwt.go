package findings

import (
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
// The structure is the one the RFCs give a compact token:
//   - the first segment is the JOSE header: a JSON object with a string `alg`
//     (RFC 7515 4.1.1, RFC 7516 4.1.1). `{}` is not a header, so `e30` and its
//     neighbours (file names, versions, host names) are not tokens;
//   - a compact JWS has three segments: header, payload (not empty), signature
//     (empty for `{"alg":"none"}`);
//   - a compact JWE has five: header, encrypted key, IV, ciphertext, tag. The
//     encrypted key is empty for direct encryption (`dir`, RFC 7516 5.1 step 5),
//     which is the usual shape of a token encrypted with a shared key.
//
// A header that also has `enc` is a JWE header, but it is accepted with either
// shape: a JWS may carry an `enc` parameter of its own, and a token that escaped
// for having one more key in its header would be the same hole as before.
const (
	// jwtMaxWork is the number of base64 characters one run may have decoded while
	// looking for a header. Every place a header could start is tried, so the work
	// grows with the square of the segment; this is what bounds it. Past the limit
	// the answer is "yes, refuse": a run that cannot be ruled out is not accepted.
	// The columns are at most 1000 bytes, so about half a million characters is the
	// most a value that passes the length check can cost; the limit exists because
	// Scan runs on the value whatever its length (the stdin cap is 64 KiB).
	jwtMaxWork = 1 << 20

	// b64urlAlphabet maps a character to its 6-bit value by position.
	b64urlAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
)

// jwtShapes is a set of the token shapes that may follow a header.
type jwtShapes uint8

const (
	shapeJWS jwtShapes = 1 << iota
	shapeJWE
)

// jwtCandidate finds the runs that could be a token: base64url characters and
// at least two dots. containsJWT decides.
var jwtCandidate = regexp.MustCompile(`[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]*){2,}`)

// containsJWT says whether a dotted run holds a JWT: verdictHit if some place in
// it starts a JOSE header that is followed by the segments its shape needs,
// verdictUnchecked if the work ran out before the answer, verdictNo otherwise. The header can
// sit after other dotted segments (`a.b.<token>`) and after any characters in its
// own segment (`prefix_<token>`, `x<token>`): `_` and `-` belong to the base64url
// alphabet and a letter is one too, so no prefix can be told from the token. That
// is why every start is tried, with no limit on how many. When the work ran out
// the answer is "refuse", but it is not "this is a token": the caller says so.
func containsJWT(run string) verdict {
	segments := strings.Split(run, ".")
	work := 0
	for i := range segments {
		possible := shapesAfter(segments, i)
		if possible == 0 {
			continue
		}
		hit, spent := headerStartsIn(segments[i], possible)
		work += spent
		if hit {
			return verdictHit
		}
		if work > jwtMaxWork {
			return verdictUnchecked
		}
	}
	return verdictNo
}

// shapesAfter returns the shapes that segments[i], taken as the header, has
// enough segments behind it to be: three with a payload that is not empty, or
// five (the second may be empty).
func shapesAfter(segments []string, i int) jwtShapes {
	var shapes jwtShapes
	if i+2 < len(segments) && segments[i+1] != "" {
		shapes |= shapeJWS
	}
	if i+4 < len(segments) {
		shapes |= shapeJWE
	}
	return shapes
}

// headerStartsIn tries every place in the segment as the start of a header and
// reports whether one is a header whose shape is among possible. spent is how
// many characters were decoded, so that the caller can keep the account.
func headerStartsIn(segment string, possible jwtShapes) (hit bool, spent int) {
	for start := 0; start < len(segment); start++ {
		candidate := segment[start:]
		if !mayStartJSONObject(candidate) {
			continue
		}
		spent += len(candidate)
		if spent > jwtMaxWork {
			return false, spent
		}
		if headerShapes(candidate)&possible != 0 {
			return true, spent
		}
	}
	return false, spent
}

// mayStartJSONObject is the cheap exact test before a decode: a JSON object
// starts with `{` after white space, so the first byte of what the text decodes
// to must be one of those five. It reads two characters, no more.
func mayStartJSONObject(s string) bool {
	if len(s) < 2 {
		return false
	}
	hi := strings.IndexByte(b64urlAlphabet, s[0])
	lo := strings.IndexByte(b64urlAlphabet, s[1])
	if hi < 0 || lo < 0 {
		return false
	}
	switch byte(hi<<2 | lo>>4) {
	case '{', ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

// headerShapes decodes s as the unpadded base64url of a JOSE header and returns
// the shapes the header allows: none if it is not a JSON object with a string
// `alg`; JWS if that is all; JWS or JWE if it also has `enc`.
func headerShapes(s string) jwtShapes {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0
	}
	var header map[string]json.RawMessage
	if json.Unmarshal(raw, &header) != nil {
		return 0
	}
	if !headerHas(header, "alg", true) {
		return 0
	}
	if headerHas(header, "enc", false) {
		return shapeJWS | shapeJWE
	}
	return shapeJWS
}

// headerHas reports whether the header has a parameter called name (and, if
// stringValue, with a string as its value). The RFCs make parameter names
// case-sensitive, but a decoder that reads the header into a struct, as Go's
// encoding/json does, matches them without regard to case; a token such a decoder
// accepts must not get through for a capital letter.
func headerHas(header map[string]json.RawMessage, name string, stringValue bool) bool {
	for key, value := range header {
		if !strings.EqualFold(key, name) {
			continue
		}
		if !stringValue || (len(value) > 0 && value[0] == '"') {
			return true
		}
	}
	return false
}
