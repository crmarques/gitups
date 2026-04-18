package resolve_test

import (
	"path/filepath"
	"testing"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
	"github.com/crmarques/gitups/internal/catalog"
	"github.com/crmarques/gitups/internal/load"
	"github.com/crmarques/gitups/internal/resolve"
)

func loadFixtures(t *testing.T) (*v1.Provision, *catalog.Catalog) {
	t.Helper()
	repoRoot, _ := filepath.Abs("../..")
	prov, err := load.Provision(filepath.Join(repoRoot, "tests/e2e/dsv/provision.yaml"))
	if err != nil {
		t.Fatalf("load provision: %v", err)
	}
	// baseDir stays at tests/e2e so the Provision's relative source path
	// (../../../gitups-packages) resolves to the sibling catalog. The
	// provision fixture itself moved one level deeper; its bytes are
	// identical to what the e2e run.sh copies into gitups-output-dir/dsv/.
	baseDir := filepath.Join(repoRoot, "tests/e2e")
	cat, err := catalog.Build(prov.Spec.Sources, baseDir)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	return prov, cat
}

func TestExpandTopoOrder(t *testing.T) {
	prov, cat := loadFixtures(t)
	fp, err := resolve.Expand(prov, cat, resolve.Options{})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}

	pos := map[string]int{}
	for i, rp := range fp.Spec.Packages {
		pos[rp.Instance] = i
	}
	for _, name := range []string{"olm", "metallb", "nginx-ingress", "metallb-config-default"} {
		if _, ok := pos[name]; !ok {
			t.Fatalf("missing %s in expanded packages: %+v", name, fp.Spec.Packages)
		}
	}
	if pos["metallb"] >= pos["nginx-ingress"] {
		t.Fatalf("metallb install must precede nginx-ingress install")
	}
	if pos["metallb"] >= pos["metallb-config-default"] {
		t.Fatalf("metallb install must precede metallb config resource")
	}
}

func TestExpandDerivesEnvResourcesFromRepoRef(t *testing.T) {
	prov, cat := loadFixtures(t)
	fp, err := resolve.Expand(prov, cat, resolve.Options{})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	got := map[string]string{}
	for _, rp := range fp.Spec.Packages {
		got[rp.Instance] = rp.RenderedPaths.Repo + "/" + rp.RenderedPaths.Dir
	}
	if got["metallb"] != "basic-infra/packages/metallb/install/helm" {
		t.Fatalf("metallb install path: %q", got["metallb"])
	}
	if got["metallb-config-default"] != "basic-infra-dsv/packages/metallb/resources/config/default" {
		t.Fatalf("derived metallb config path: %q", got["metallb-config-default"])
	}
	if got["argocd-applications-root"] != "gitops-controllers-dsv/packages/argocd/resources/applications/root" {
		t.Fatalf("derived argocd applications path: %q", got["argocd-applications-root"])
	}
}

func TestExpandPlaceholders(t *testing.T) {
	prov, cat := loadFixtures(t)
	fp, err := resolve.Expand(prov, cat, resolve.Options{})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}

	want := map[string]bool{
		"spec.packages[argocd-applications-root].resolvedValues.repoURL":                false,
		"spec.packages[declarest-config-default].resolvedValues.repoURL":                false,
		"spec.packages[keycloak].resolvedValues.adminPassword":                          false,
		"spec.packages[metallb-config-default].resolvedValues.addressPools[0].cidrs[0]": false,
		"spec.packages[vault].resolvedValues.devRootToken":                              false,
	}
	for _, ph := range fp.Spec.Placeholders {
		if _, ok := want[ph.Path]; ok {
			want[ph.Path] = true
		}
	}
	for path, found := range want {
		if !found {
			t.Errorf("missing expected placeholder: %s", path)
		}
	}
}

func TestExpandIdempotentPreservesUserEdits(t *testing.T) {
	prov, cat := loadFixtures(t)
	first, err := resolve.Expand(prov, cat, resolve.Options{})
	if err != nil {
		t.Fatalf("first expand: %v", err)
	}

	for i := range first.Spec.Packages {
		rp := &first.Spec.Packages[i]
		switch rp.Instance {
		case "argocd-applications-root", "declarest-config-default":
			rp.ResolvedValues["repoURL"] = "https://git.example.com/gitops.git"
		case "keycloak":
			rp.ResolvedValues["adminPassword"] = "keycloak-test"
		case "metallb-config-default":
			pools := rp.ResolvedValues["addressPools"].([]any)
			pool := pools[0].(map[string]any)
			pool["cidrs"] = []any{"10.0.0.1-10.0.0.9"}
		case "vault":
			rp.ResolvedValues["devRootToken"] = "vault-test"
		}
	}

	second, err := resolve.Expand(prov, cat, resolve.Options{Prior: first})
	if err != nil {
		t.Fatalf("re-expand: %v", err)
	}

	for _, rp := range second.Spec.Packages {
		switch rp.Instance {
		case "argocd-applications-root":
			if got := rp.ResolvedValues["repoURL"]; got != "https://git.example.com/gitops.git" {
				t.Errorf("argocd.repoURL not preserved: got %v", got)
			}
		case "metallb-config-default":
			pool := rp.ResolvedValues["addressPools"].([]any)[0].(map[string]any)
			cidrs := pool["cidrs"].([]any)
			if cidrs[0] != "10.0.0.1-10.0.0.9" {
				t.Errorf("metallb CIDR not preserved: got %v", cidrs)
			}
		}
	}
	if len(second.Spec.Placeholders) != 0 {
		t.Errorf("expected empty placeholders after user edits, got %+v", second.Spec.Placeholders)
	}
}

func TestExpandForcePreservesPlaceholderFills(t *testing.T) {
	prov, cat := loadFixtures(t)
	first, err := resolve.Expand(prov, cat, resolve.Options{})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	for i := range first.Spec.Packages {
		if first.Spec.Packages[i].Instance == "argocd-applications-root" {
			first.Spec.Packages[i].ResolvedValues["repoURL"] = "https://user-edit.example.com/"
		}
	}
	second, err := resolve.Expand(prov, cat, resolve.Options{Prior: first, Force: true})
	if err != nil {
		t.Fatalf("force expand: %v", err)
	}
	for _, rp := range second.Spec.Packages {
		if rp.Instance != "argocd-applications-root" {
			continue
		}
		if got := rp.ResolvedValues["repoURL"]; got != "https://user-edit.example.com/" {
			t.Errorf("force expand should preserve user-authored fill, got %v", got)
		}
	}
}

func TestExpandRepeatedExplicitResources(t *testing.T) {
	prov, cat := loadFixtures(t)
	prov.Spec.Repositories = []v1.RepositoryDecl{
		{
			Name: "base",
			Type: "k8s-gitops-generic",
			Packages: []v1.PackageRef{
				{Template: "local/metallb", InstallMethod: "helm"},
			},
		},
		{
			Name: "base-dsv",
			Type: "k8s-gitops-env",
			RepoRef: &v1.RepositoryRef{
				Name:   "base",
				Commit: "v0.0.1",
			},
			Packages: []v1.PackageRef{
				{
					Template: "local/metallb",
					Resources: []v1.ResourceRef{
						{
							Template: "config",
							Name:     "pool-a",
							Values: map[string]any{
								"addressPools": []any{map[string]any{"name": "a", "cidrs": []any{"10.0.0.1-10.0.0.2"}}},
							},
						},
						{
							Template: "config",
							Name:     "pool-b",
							Values: map[string]any{
								"addressPools": []any{map[string]any{"name": "b", "cidrs": []any{"10.0.1.1-10.0.1.2"}}},
							},
						},
					},
				},
			},
		},
	}
	fp, err := resolve.Expand(prov, cat, resolve.Options{})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	got := map[string]string{}
	for _, rp := range fp.Spec.Packages {
		got[rp.Instance] = rp.RenderedPaths.Dir
	}
	if got["metallb-config-pool-a"] != "packages/metallb/resources/config/pool-a" {
		t.Fatalf("pool-a path mismatch: %+v", got)
	}
	if got["metallb-config-pool-b"] != "packages/metallb/resources/config/pool-b" {
		t.Fatalf("pool-b path mismatch: %+v", got)
	}
}

func TestExpandRejectsUnknownTemplate(t *testing.T) {
	prov, cat := loadFixtures(t)
	prov.Spec.Repositories = []v1.RepositoryDecl{
		{
			Name: "base",
			Type: "k8s-gitops-generic",
			Packages: []v1.PackageRef{
				{Template: "local/no-such-pkg"},
			},
		},
	}
	_, err := resolve.Expand(prov, cat, resolve.Options{})
	if err == nil {
		t.Fatal("expected error for unknown template")
	}
}

func TestExpandEnvKeyOverridesMetadataName(t *testing.T) {
	prov, cat := loadFixtures(t)
	prov.Metadata.Name = "basic-infra-dev"
	prov.Spec.EnvKey = "dev"
	fp, err := resolve.Expand(prov, cat, resolve.Options{})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	var seen string
	for _, rp := range fp.Spec.Packages {
		if rp.Instance == "metallb-config-default" {
			seen = rp.Repository
			break
		}
	}
	if seen != "basic-infra-dev" {
		t.Errorf("metallb config repository: got %q want %q", seen, "basic-infra-dev")
	}
}

func TestExpandRecordsExtendedFrom(t *testing.T) {
	prov, cat := loadFixtures(t)
	ef := &v1.ExtendedFrom{Source: "../basic-infra/provision.yaml", Ref: "v1.4.0"}
	fp, err := resolve.Expand(prov, cat, resolve.Options{ExtendedFrom: ef})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if fp.Spec.ExtendedFrom == nil || fp.Spec.ExtendedFrom.Source != ef.Source || fp.Spec.ExtendedFrom.Ref != ef.Ref {
		t.Errorf("ExtendedFrom not persisted: %+v", fp.Spec.ExtendedFrom)
	}
}
