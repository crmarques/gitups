package resolve_test

import (
	"strings"
	"testing"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
	"github.com/crmarques/gitups/internal/resolve"
)

// withBinding attaches an argocd ↔ gitea binding to the argocd entry in the
// dsv provision. Reused by every binding test so the wiring is authored in
// one place.
func withBinding(prov *v1.Provision, bindingName string) {
	for ri := range prov.Spec.Repositories {
		repo := &prov.Spec.Repositories[ri]
		if repo.Name != "gitops-controllers" {
			continue
		}
		for pi := range repo.Packages {
			pr := &repo.Packages[pi]
			if pr.Template != "local/argocd" {
				continue
			}
			pr.Bindings = []v1.BindingRef{{
				Name:       bindingName,
				Capability: "git-provider",
				Provider:   v1.ProviderRef{Repo: "support-services", Instance: "gitea"},
				Values:     map[string]any{"username": "argocd-bot"},
			}}
		}
	}
}

// findBindingUnits returns the consumer and all provider ResolvedPackages for
// bindingName, indexed by their instance. Consumer entries get the
// "consumer" side key, providers are keyed by instance.
func findBindingUnits(fp *v1.FullProvision, bindingName string) (consumer *v1.ResolvedPackage, providers []*v1.ResolvedPackage) {
	for i := range fp.Spec.Packages {
		rp := &fp.Spec.Packages[i]
		if rp.Binding == nil || rp.Binding.Name != bindingName {
			continue
		}
		switch rp.Binding.Side {
		case "consumer":
			consumer = rp
		case "provider":
			providers = append(providers, rp)
		}
	}
	return consumer, providers
}

func TestExpandBindingFanOutPerEnv(t *testing.T) {
	prov, cat := loadFixtures(t)
	// Replace repositories with a minimal shape so we can author multiple env
	// siblings of support-services without triggering default-resource
	// collisions from the broader fixture. Each env carries empty explicit
	// Packages so nothing auto-derives off the generic's defaults.
	prov.Spec.Repositories = []v1.RepositoryDecl{
		{
			Name: "support-services",
			Type: "kubernetes-resources",
			Packages: []v1.PackageRef{
				{Template: "local/gitea"},
			},
		},
		{
			Name:     "support-services-dsv",
			Type:     "kubernetes-resources",
			RepoRef:  &v1.RepositoryRef{Name: "support-services"},
			Packages: []v1.PackageRef{{Template: "local/gitea", Resources: []v1.ResourceRef{}}},
		},
		{
			Name:     "support-services-alt",
			Type:     "kubernetes-resources",
			RepoRef:  &v1.RepositoryRef{Name: "support-services"},
			Packages: []v1.PackageRef{{Template: "local/gitea", Instance: "gitea-alt", Resources: []v1.ResourceRef{}}},
		},
		{
			Name: "gitops-controllers",
			Type: "kubernetes-resources",
			Packages: []v1.PackageRef{
				{Template: "local/argocd", InstallMethod: "helm"},
				{Template: "local/declarest"},
			},
		},
		{
			Name:    "gitops-controllers-dsv",
			Type:    "kubernetes-resources",
			RepoRef: &v1.RepositoryRef{Name: "gitops-controllers"},
			Packages: []v1.PackageRef{{
				Template:      "local/argocd",
				InstallMethod: "helm",
				Resources:     []v1.ResourceRef{},
				Bindings: []v1.BindingRef{{
					Name:       "argocd-gitea-bot",
					Capability: "git-provider",
					Provider:   v1.ProviderRef{Repo: "support-services", Instance: "gitea"},
					Values:     map[string]any{"username": "argocd-bot"},
				}},
			}},
		},
	}

	fp, err := resolve.Expand(prov, cat, resolve.Options{})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	_, providers := findBindingUnits(fp, "argocd-gitea-bot")
	// Only one env repo references the actual provider instance "gitea" —
	// support-services-alt hosts gitea-alt. Fan-out still produces one unit
	// per env repo that references the provider's generic repo: two envs.
	if len(providers) != 2 {
		t.Fatalf("expected 2 provider units (one per env repo referencing provider generic), got %d", len(providers))
	}
	// Fan-out ordering must be stable: sorted by env repo name.
	if providers[0].Repository >= providers[1].Repository {
		t.Errorf("provider units must be ordered by env repo name, got %q then %q",
			providers[0].Repository, providers[1].Repository)
	}
	// Identical instance names across envs would collide — the env-repo
	// suffix in resource names keeps them distinct.
	if providers[0].Instance == providers[1].Instance {
		t.Errorf("provider instances should be env-scoped, got duplicate %q", providers[0].Instance)
	}
}

func TestExpandBindingExportPropagation(t *testing.T) {
	prov, cat := loadFixtures(t)
	withBinding(prov, "argocd-gitea-bot")

	fp, err := resolve.Expand(prov, cat, resolve.Options{})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	consumer, providers := findBindingUnits(fp, "argocd-gitea-bot")
	if consumer == nil || len(providers) != 1 {
		t.Fatalf("setup: consumer=%v providers=%d", consumer != nil, len(providers))
	}
	provider := providers[0]

	// Non-secret exports are literal and identical on both sides.
	if got := provider.ResolvedValues["url"]; got != consumer.ResolvedValues["repoURL"] {
		t.Errorf("url export mismatch: provider=%v consumer=%v", got, consumer.ResolvedValues["repoURL"])
	}
	if got := consumer.ResolvedValues["repoUsername"]; got != "argocd-bot" {
		t.Errorf("expected consumer.repoUsername=argocd-bot, got %v", got)
	}
	if got := provider.ResolvedValues["username"]; got != "argocd-bot" {
		t.Errorf("expected provider.username=argocd-bot, got %v", got)
	}

	// Secret exports render as the placeholder sentinel on both sides.
	if got := provider.ResolvedValues["token"]; got != v1.PlaceholderSentinel {
		t.Errorf("expected provider.token to be placeholder, got %v", got)
	}
	if got := consumer.ResolvedValues["repoPassword"]; got != v1.PlaceholderSentinel {
		t.Errorf("expected consumer.repoPassword to be placeholder, got %v", got)
	}
}

func TestExpandBindingIdempotentPreservesSecretFill(t *testing.T) {
	prov, cat := loadFixtures(t)
	withBinding(prov, "argocd-gitea-bot")

	first, err := resolve.Expand(prov, cat, resolve.Options{})
	if err != nil {
		t.Fatalf("first expand: %v", err)
	}
	_, providers := findBindingUnits(first, "argocd-gitea-bot")
	if len(providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(providers))
	}
	providers[0].ResolvedValues["token"] = "my-filled-token"

	second, err := resolve.Expand(prov, cat, resolve.Options{Prior: first})
	if err != nil {
		t.Fatalf("re-expand: %v", err)
	}
	consumer2, providers2 := findBindingUnits(second, "argocd-gitea-bot")
	if consumer2 == nil || len(providers2) != 1 {
		t.Fatalf("setup: consumer=%v providers=%d", consumer2 != nil, len(providers2))
	}
	if got := providers2[0].ResolvedValues["token"]; got != "my-filled-token" {
		t.Errorf("provider token fill not preserved, got %v", got)
	}
	if got := consumer2.ResolvedValues["repoPassword"]; got != "my-filled-token" {
		t.Errorf("consumer repoPassword should mirror provider fill, got %v", got)
	}
}

func TestExpandBindingRejectsUnresolvedProvider(t *testing.T) {
	prov, cat := loadFixtures(t)
	for ri := range prov.Spec.Repositories {
		repo := &prov.Spec.Repositories[ri]
		if repo.Name != "gitops-controllers" {
			continue
		}
		for pi := range repo.Packages {
			pr := &repo.Packages[pi]
			if pr.Template != "local/argocd" {
				continue
			}
			pr.Bindings = []v1.BindingRef{{
				Name:       "argocd-gitea-bot",
				Capability: "git-provider",
				Provider:   v1.ProviderRef{Repo: "support-services", Instance: "nobody"},
				Values:     map[string]any{"username": "argocd-bot"},
			}}
		}
	}
	if _, err := resolve.Expand(prov, cat, resolve.Options{}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected provider-not-found error, got %v", err)
	}
}

func TestExpandBindingRejectsMissingBindingValue(t *testing.T) {
	prov, cat := loadFixtures(t)
	withBinding(prov, "argocd-gitea-bot")
	// Drop the binding.values.username the provider export expects.
	for ri := range prov.Spec.Repositories {
		repo := &prov.Spec.Repositories[ri]
		if repo.Name != "gitops-controllers" {
			continue
		}
		for pi := range repo.Packages {
			pr := &repo.Packages[pi]
			if pr.Template == "local/argocd" {
				pr.Bindings[0].Values = nil
			}
		}
	}
	if _, err := resolve.Expand(prov, cat, resolve.Options{}); err == nil || !strings.Contains(err.Error(), "username") {
		t.Fatalf("expected missing-binding-value error, got %v", err)
	}
}

func TestExpandBindingRejectsDuplicateBindingName(t *testing.T) {
	prov, cat := loadFixtures(t)
	withBinding(prov, "dup")
	// Wire a second consumer in gitops-controllers-{{.Env}} reusing the same
	// binding name — must fail.
	for ri := range prov.Spec.Repositories {
		repo := &prov.Spec.Repositories[ri]
		if repo.Name != "gitops-controllers-{{.Env}}" {
			continue
		}
		repo.Packages = append(repo.Packages, v1.PackageRef{
			Template: "local/argocd",
			Instance: "argocd-other",
			Bindings: []v1.BindingRef{{
				Name:       "dup",
				Capability: "git-provider",
				Provider:   v1.ProviderRef{Repo: "support-services", Instance: "gitea"},
				Values:     map[string]any{"username": "x"},
			}},
		})
	}
	if _, err := resolve.Expand(prov, cat, resolve.Options{}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate-binding-name error, got %v", err)
	}
}

func TestExpandBindingApplyWavePlacesConsumerAfterProvider(t *testing.T) {
	prov, cat := loadFixtures(t)
	withBinding(prov, "argocd-gitea-bot")

	fp, err := resolve.Expand(prov, cat, resolve.Options{})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	consumer, providers := findBindingUnits(fp, "argocd-gitea-bot")
	if consumer == nil || len(providers) == 0 {
		t.Fatalf("setup: consumer=%v providers=%d", consumer != nil, len(providers))
	}
	for _, p := range providers {
		if consumer.ApplyWave <= p.ApplyWave {
			t.Errorf("consumer apply-wave %d must be greater than provider %q wave %d",
				consumer.ApplyWave, p.Instance, p.ApplyWave)
		}
	}
}
