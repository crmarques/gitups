// Package secrets generates values for FullProvision placeholders whose
// Placeholder entry declared a Generator. Pure function over the
// generator spec; the caller is responsible for writing the value back
// into resolvedValues. Entropy comes from crypto/rand.
package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
)

const (
	defaultRandomHexLength    = 32
	defaultRandomBase64Length = 32
	maxLength                 = 256
)

// Generate produces a value matching the supplied generator spec. Errors
// are returned for unknown kinds and out-of-range lengths so callers
// surface bad descriptors at apply time even if load-time validation was
// somehow skipped.
func Generate(g v1.Generator) (string, error) {
	switch g.Kind {
	case v1.GeneratorRandomHex:
		n := g.Length
		if n == 0 {
			n = defaultRandomHexLength
		}
		if n < 0 || n > maxLength {
			return "", fmt.Errorf("randomHex: length %d out of range (0-%d)", n, maxLength)
		}
		if n%2 != 0 {
			return "", fmt.Errorf("randomHex: length %d must be even", n)
		}
		buf := make([]byte, n/2)
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("randomHex: read entropy: %w", err)
		}
		return hex.EncodeToString(buf), nil
	case v1.GeneratorRandomBase64:
		n := g.Length
		if n == 0 {
			n = defaultRandomBase64Length
		}
		if n < 0 || n > maxLength {
			return "", fmt.Errorf("randomBase64: length %d out of range (0-%d)", n, maxLength)
		}
		// Read enough entropy to encode at least n base64 chars (each
		// base64 char encodes 6 bits). Truncate the encoded string to
		// exactly n characters; the trailing chars still carry full
		// entropy because we draw fresh randomness.
		raw := make([]byte, (n*6+7)/8)
		if _, err := rand.Read(raw); err != nil {
			return "", fmt.Errorf("randomBase64: read entropy: %w", err)
		}
		enc := base64.RawURLEncoding.EncodeToString(raw)
		if len(enc) < n {
			return "", fmt.Errorf("randomBase64: encoded %d chars, want %d", len(enc), n)
		}
		return enc[:n], nil
	case v1.GeneratorUUID:
		var buf [16]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return "", fmt.Errorf("uuid: read entropy: %w", err)
		}
		// RFC 4122 v4: set version (4) and variant (10).
		buf[6] = (buf[6] & 0x0f) | 0x40
		buf[8] = (buf[8] & 0x3f) | 0x80
		return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
			buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
	case "":
		return "", fmt.Errorf("generator: kind is required")
	default:
		return "", fmt.Errorf("generator: unknown kind %q", g.Kind)
	}
}
