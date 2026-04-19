package secrets_test

import (
	"regexp"
	"testing"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
	"github.com/crmarques/gitups/internal/secrets"
)

func TestGenerateRandomHex(t *testing.T) {
	cases := []struct {
		name   string
		length int
		want   int
	}{
		{"default", 0, 32},
		{"custom even", 16, 16},
		{"upper bound", 256, 256},
	}
	hexRe := regexp.MustCompile(`^[0-9a-f]+$`)
	for _, c := range cases {
		got, err := secrets.Generate(v1.Generator{Kind: v1.GeneratorRandomHex, Length: c.length})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(got) != c.want {
			t.Errorf("%s: length got %d want %d", c.name, len(got), c.want)
		}
		if !hexRe.MatchString(got) {
			t.Errorf("%s: not hex: %q", c.name, got)
		}
	}
}

func TestGenerateRandomBase64(t *testing.T) {
	got, err := secrets.Generate(v1.Generator{Kind: v1.GeneratorRandomBase64, Length: 24})
	if err != nil {
		t.Fatalf("randomBase64: %v", err)
	}
	if len(got) != 24 {
		t.Errorf("length got %d want 24", len(got))
	}
	// URL-safe base64 alphabet only.
	re := regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	if !re.MatchString(got) {
		t.Errorf("non base64: %q", got)
	}
}

func TestGenerateUUID(t *testing.T) {
	got, err := secrets.Generate(v1.Generator{Kind: v1.GeneratorUUID})
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	uuidRe := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !uuidRe.MatchString(got) {
		t.Errorf("not uuid v4: %q", got)
	}
}

func TestGenerateUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 64 {
		v, err := secrets.Generate(v1.Generator{Kind: v1.GeneratorRandomHex, Length: 16})
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if seen[v] {
			t.Fatalf("duplicate output %q within 64 draws", v)
		}
		seen[v] = true
	}
}

func TestGenerateErrors(t *testing.T) {
	cases := []struct {
		name string
		gen  v1.Generator
	}{
		{"unknown kind", v1.Generator{Kind: "magic"}},
		{"empty kind", v1.Generator{}},
		{"hex odd length", v1.Generator{Kind: v1.GeneratorRandomHex, Length: 7}},
		{"hex too long", v1.Generator{Kind: v1.GeneratorRandomHex, Length: 1024}},
		{"base64 too long", v1.Generator{Kind: v1.GeneratorRandomBase64, Length: 1024}},
	}
	for _, c := range cases {
		if _, err := secrets.Generate(c.gen); err == nil {
			t.Errorf("%s: expected error, got nil", c.name)
		}
	}
}
