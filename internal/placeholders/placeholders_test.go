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
	phs := placeholders.Scan("metallb", values, reasons, map[string]bool{"repoURL": true}, nil)

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

func TestScanCarriesGenerator(t *testing.T) {
	values := map[string]any{
		"adminPassword": v1.PlaceholderSentinel,
		"chart": map[string]any{
			"apiKey": v1.PlaceholderSentinel,
		},
		"manualToken": v1.PlaceholderSentinel,
	}
	reasons := map[string]string{
		"adminPassword": "initial admin password",
		"manualToken":   "user-supplied token",
	}
	sensitive := map[string]bool{"adminPassword": true, "manualToken": true}
	gen := &v1.Generator{Kind: v1.GeneratorRandomBase64, Length: 32}
	chartGen := &v1.Generator{Kind: v1.GeneratorUUID}
	generators := map[string]*v1.Generator{
		"adminPassword": gen,
		"chart":         chartGen, // top-level prefix; chart.apiKey should resolve to it
	}

	phs := placeholders.Scan("argocd", values, reasons, sensitive, generators)

	if len(phs) != 3 {
		t.Fatalf("want 3 placeholders, got %d: %+v", len(phs), phs)
	}
	got := map[string]*v1.Generator{}
	for _, ph := range phs {
		got[ph.Path] = ph.Generator
	}
	if g := got["spec.packages[argocd].resolvedValues.adminPassword"]; g != gen {
		t.Errorf("adminPassword generator not carried; got %+v", g)
	}
	if g := got["spec.packages[argocd].resolvedValues.chart.apiKey"]; g != chartGen {
		t.Errorf("chart.apiKey did not inherit chart's generator; got %+v", g)
	}
	if g := got["spec.packages[argocd].resolvedValues.manualToken"]; g != nil {
		t.Errorf("manualToken should have no generator; got %+v", g)
	}
}
