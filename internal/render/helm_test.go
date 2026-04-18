package render

import "testing"

func TestResolveHelmVersionUsesResolvedChartVersion(t *testing.T) {
	got := resolveHelmVersion("1.0.0", map[string]any{
		"chart": map[string]any{"version": "2.0.0"},
	})
	if got != "2.0.0" {
		t.Fatalf("version = %q, want 2.0.0", got)
	}
}

func TestResolveHelmVersionFallsBackToDescriptor(t *testing.T) {
	got := resolveHelmVersion("1.0.0", map[string]any{
		"chart": map[string]any{},
	})
	if got != "1.0.0" {
		t.Fatalf("version = %q, want 1.0.0", got)
	}
}
