package placeholders_test

import (
	"testing"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
	"github.com/crmarques/gitups/internal/placeholders"
)

func TestContains(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want bool
	}{
		{"plain string", "hello", false},
		{"sentinel string", v1.PlaceholderSentinel, true},
		{"nested map", map[string]any{"a": map[string]any{"b": v1.PlaceholderSentinel}}, true},
		{"nested array", map[string]any{"a": []any{"ok", v1.PlaceholderSentinel}}, true},
		{"no sentinel", map[string]any{"a": map[string]any{"b": "ok"}}, false},
	}
	for _, c := range cases {
		if got := placeholders.Contains(c.in); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestScanSortsAndAnnotates(t *testing.T) {
	values := map[string]any{
		"repoURL": v1.PlaceholderSentinel,
		"addressPools": []any{
			map[string]any{"name": "default", "cidrs": []any{v1.PlaceholderSentinel}},
		},
	}
	reasons := map[string]string{
		"repoURL":      "repo URL",
		"addressPools": "CIDR",
	}
	phs := placeholders.Scan("metallb", values, reasons, map[string]bool{"repoURL": true})

	if len(phs) != 2 {
		t.Fatalf("want 2 placeholders, got %d: %+v", len(phs), phs)
	}
	// sorted by path
	if phs[0].Path >= phs[1].Path {
		t.Errorf("not sorted: %+v", phs)
	}
	// prefix-match reason lookup: the CIDR sentinel (deep in addressPools)
	// should still receive the "CIDR" reason annotated on addressPools.
	for _, ph := range phs {
		if ph.Reason == "" {
			t.Errorf("empty reason on %s", ph.Path)
		}
	}
}
