package catalog_test

import (
	"path/filepath"
	"testing"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
	"github.com/crmarques/gitups/internal/catalog"
)

func TestCatalogBuildFilesystem(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	sources := []v1.PackageSource{
		{Name: "local", Type: "filesystem", Path: "../gitups-packages"},
	}
	cat, err := catalog.Build(sources, repoRoot)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, want := range []string{"local/argocd", "local/metallb", "local/nginx-ingress"} {
		if _, ok := cat.Lookup(want); !ok {
			t.Errorf("missing entry %q (known: %v)", want, cat.Qualified())
		}
	}
}

func TestCatalogRejectsUnknownSourceType(t *testing.T) {
	_, err := catalog.Build([]v1.PackageSource{{Name: "x", Type: "git", Path: "."}}, ".")
	if err == nil {
		t.Fatal("expected error for unsupported source type")
	}
}
