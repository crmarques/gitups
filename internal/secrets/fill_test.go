package secrets_test

import (
	"strings"
	"testing"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
	"github.com/crmarques/gitups/internal/secrets"
)

func TestFillReplacesGeneratorPlaceholders(t *testing.T) {
	gen := &v1.Generator{Kind: v1.GeneratorRandomBase64, Length: 16}
	fp := &v1.FullProvision{
		Spec: v1.FullProvisionSpec{
			Packages: []v1.ResolvedPackage{
				{
					Instance: "argocd",
					ResolvedValues: map[string]any{
						"adminPassword": v1.PlaceholderSentinel,
						"manualToken":   v1.PlaceholderSentinel,
						"chart": map[string]any{
							"apiKey": v1.PlaceholderSentinel,
						},
					},
				},
				{
					Instance: "metallb-config",
					ResolvedValues: map[string]any{
						"addressPools": []any{
							map[string]any{
								"cidrs": []any{v1.PlaceholderSentinel},
							},
						},
					},
				},
			},
			Placeholders: []v1.Placeholder{
				{
					Path:      "spec.packages[argocd].resolvedValues.adminPassword",
					Reason:    "auto",
					Sensitive: true,
					Generator: gen,
				},
				{
					Path:      "spec.packages[argocd].resolvedValues.manualToken",
					Reason:    "manual",
					Sensitive: true,
				},
				{
					Path:      "spec.packages[argocd].resolvedValues.chart.apiKey",
					Reason:    "auto",
					Sensitive: true,
					Generator: &v1.Generator{Kind: v1.GeneratorUUID},
				},
				{
					Path:      "spec.packages[metallb-config].resolvedValues.addressPools[0].cidrs[0]",
					Reason:    "manual",
					Sensitive: false,
				},
			},
		},
	}

	results, err := secrets.Fill(fp)
	if err != nil {
		t.Fatalf("fill: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 fills, got %d (%+v)", len(results), results)
	}
	for _, r := range results {
		if r.Path == "" || r.Kind == "" {
			t.Errorf("incomplete result: %+v", r)
		}
	}

	// Generator-bearing placeholders gone, manual ones preserved.
	if len(fp.Spec.Placeholders) != 2 {
		t.Fatalf("want 2 remaining placeholders, got %d", len(fp.Spec.Placeholders))
	}
	for _, ph := range fp.Spec.Placeholders {
		if ph.Generator != nil {
			t.Errorf("generator-bearing placeholder still present: %+v", ph)
		}
	}

	// Generated values have replaced sentinels.
	rv := fp.Spec.Packages[0].ResolvedValues
	if v := rv["adminPassword"]; v == v1.PlaceholderSentinel || v == nil {
		t.Errorf("adminPassword not filled: %v", v)
	}
	if v, _ := rv["adminPassword"].(string); len(v) != 16 {
		t.Errorf("adminPassword length got %d want 16 (got %q)", len(v), v)
	}
	chart := rv["chart"].(map[string]any)
	if v := chart["apiKey"]; v == v1.PlaceholderSentinel || v == nil {
		t.Errorf("chart.apiKey not filled: %v", v)
	}
	if v, _ := chart["apiKey"].(string); !strings.Contains(v, "-") {
		t.Errorf("chart.apiKey not uuid-shaped: %q", v)
	}
	// manualToken stays as sentinel (no generator).
	if rv["manualToken"] != v1.PlaceholderSentinel {
		t.Errorf("manualToken should stay sentinel, got %v", rv["manualToken"])
	}
	// Array index placeholder stays as sentinel because no generator.
	pool := fp.Spec.Packages[1].ResolvedValues["addressPools"].([]any)[0].(map[string]any)
	if pool["cidrs"].([]any)[0] != v1.PlaceholderSentinel {
		t.Errorf("metallb cidr should stay sentinel: %v", pool["cidrs"])
	}
}

func TestFillArrayIndexPath(t *testing.T) {
	fp := &v1.FullProvision{
		Spec: v1.FullProvisionSpec{
			Packages: []v1.ResolvedPackage{
				{
					Instance: "metallb-config",
					ResolvedValues: map[string]any{
						"addressPools": []any{
							map[string]any{
								"cidrs": []any{v1.PlaceholderSentinel},
							},
						},
					},
				},
			},
			Placeholders: []v1.Placeholder{
				{
					Path:      "spec.packages[metallb-config].resolvedValues.addressPools[0].cidrs[0]",
					Generator: &v1.Generator{Kind: v1.GeneratorRandomHex, Length: 8},
				},
			},
		},
	}

	if _, err := secrets.Fill(fp); err != nil {
		t.Fatalf("fill: %v", err)
	}
	pool := fp.Spec.Packages[0].ResolvedValues["addressPools"].([]any)[0].(map[string]any)
	v, _ := pool["cidrs"].([]any)[0].(string)
	if len(v) != 8 {
		t.Errorf("filled cidr length got %d want 8 (%q)", len(v), v)
	}
	if len(fp.Spec.Placeholders) != 0 {
		t.Errorf("placeholder list should be empty, got %+v", fp.Spec.Placeholders)
	}
}

func TestFillMissingInstance(t *testing.T) {
	fp := &v1.FullProvision{
		Spec: v1.FullProvisionSpec{
			Placeholders: []v1.Placeholder{
				{
					Path:      "spec.packages[ghost].resolvedValues.x",
					Generator: &v1.Generator{Kind: v1.GeneratorRandomHex},
				},
			},
		},
	}
	if _, err := secrets.Fill(fp); err == nil {
		t.Fatal("expected error for missing instance, got nil")
	}
}
