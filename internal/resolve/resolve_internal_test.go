package resolve

import (
	"testing"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
	"github.com/crmarques/gitups/internal/catalog"
)

// TestResolveOneCarriesGeneratorOnPlaceholder is the focused unit test
// for the generator-on-placeholder wiring added with apply-time
// autogen. It bypasses Expand so the assertion stays small and
// independent of any sibling-repo fixtures.
func TestResolveOneCarriesGeneratorOnPlaceholder(t *testing.T) {
	gen := &v1.Generator{Kind: v1.GeneratorRandomBase64, Length: 32}
	def := &v1.PackageDefinition{
		Metadata: v1.PackageMeta{Name: "demo", Version: "0.0.1"},
		Spec:     v1.PackageDefinitionSpec{Role: v1.RoleWorkload},
	}
	unit := catalog.Unit{
		Name: "raw",
		Descriptor: v1.PackageDescriptorSpec{
			Renderer: v1.RendererRaw,
			Inputs: []v1.InputSpec{
				{
					Name:              "adminPassword",
					Type:              "string",
					Placeholder:       true,
					Sensitive:         true,
					PlaceholderReason: "auto-generated at apply",
					Generator:         gen,
				},
				{
					Name:              "manualToken",
					Type:              "string",
					Placeholder:       true,
					Sensitive:         true,
					PlaceholderReason: "user-supplied",
				},
			},
		},
	}
	r := ref{
		template:        "local/demo",
		packageInstance: "demo",
		instance:        "demo",
		repository:      "infra",
		unitType:        v1.UnitTypeInstall,
		domain:          v1.DomainInstall,
		installMethod:   "raw",
		entry:           catalog.Entry{Def: def},
		unit:            unit,
	}

	rp, phs, err := resolveOne(r, nil, false)
	if err != nil {
		t.Fatalf("resolveOne: %v", err)
	}
	if rp.ResolvedValues["adminPassword"] != v1.PlaceholderSentinel {
		t.Fatalf("adminPassword should be sentinel, got %v", rp.ResolvedValues["adminPassword"])
	}
	got := map[string]*v1.Generator{}
	for _, ph := range phs {
		got[ph.Path] = ph.Generator
	}
	if g := got["spec.packages[demo].resolvedValues.adminPassword"]; g != gen {
		t.Errorf("adminPassword should carry generator; got %+v", g)
	}
	if g := got["spec.packages[demo].resolvedValues.manualToken"]; g != nil {
		t.Errorf("manualToken should have nil generator; got %+v", g)
	}
}
