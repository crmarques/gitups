// Controllers phase: materializes KRC managed-repo units from
// Provision.spec.controllers.kubernetesResources. For every env repo that
// references the KRC's generic repo, synthesises one managed-repo
// ResolvedPackage per output repo — except the KRC's generic repo itself
// (gitups direct-applies that during bootstrap).
//
// Runs after resolveBindings so the controller-owned registration units
// see the full tree, including binding-synthesised resources.

package resolve

import (
	"fmt"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
	"github.com/crmarques/gitups/internal/catalog"
)

const intentManagedRepo = "managed-repo"

func resolveControllers(
	p *v1.Provision,
	cat *catalog.Catalog,
	envKey string,
	fp *v1.FullProvision,
	priorByInstance map[string]*v1.ResolvedPackage,
	forceMode bool,
) ([]v1.Placeholder, error) {
	if p.Spec.Controllers == nil || p.Spec.Controllers.KubernetesResources == nil {
		return nil, nil
	}
	assignment := p.Spec.Controllers.KubernetesResources

	entry, template, instance, err := findControllerPackage(p, cat, assignment.Repo, assignment.Instance)
	if err != nil {
		return nil, fmt.Errorf("spec.controllers.kubernetesResources: %w", err)
	}
	if entry.Def.Spec.Role != v1.RoleKRC {
		return nil, fmt.Errorf("spec.controllers.kubernetesResources: package %q has role %q; expected %q",
			entry.Def.Metadata.Name, entry.Def.Spec.Role, v1.RoleKRC)
	}
	unit, ok := entry.LookupDomainUnit(v1.DomainKRC, intentManagedRepo)
	if !ok {
		return nil, fmt.Errorf("spec.controllers.kubernetesResources: package %q does not implement the %q intent (missing %s/%s/)",
			entry.Def.Metadata.Name, intentManagedRepo, v1.DomainKRC, intentManagedRepo)
	}

	krcGenericRepo := assignment.Repo

	// KRC env repos are the env repos that reference the KRC's generic
	// repo via repoRef. Each receives one managed-repo unit per output
	// repo (except the KRC's generic).
	var krcEnvRepos []v1.RepositoryDecl
	for _, r := range p.Spec.Repositories {
		if r.Type == "k8s-gitops-env" && r.RepoRef != nil && r.RepoRef.Name == krcGenericRepo {
			krcEnvRepos = append(krcEnvRepos, r)
		}
	}
	if len(krcEnvRepos) == 0 {
		return nil, fmt.Errorf("spec.controllers.kubernetesResources: no env repository references generic repo %q; the KRC has no home for managed-repo units",
			krcGenericRepo)
	}

	// Unique output repo names in deterministic order (same order as
	// spec.repositories). Env-substituted names.
	var outputRepos []string
	seen := map[string]bool{}
	for _, r := range p.Spec.Repositories {
		name := substituteEnv(r.Name, envKey)
		if seen[name] {
			continue
		}
		seen[name] = true
		outputRepos = append(outputRepos, name)
	}

	var allPhs []v1.Placeholder
	for _, envRepo := range krcEnvRepos {
		krcEnvRepoName := substituteEnv(envRepo.Name, envKey)
		for _, target := range outputRepos {
			if target == krcGenericRepo {
				// KRC install lives here — gitups direct-applies it
				// during bootstrap; the KRC cannot reconcile itself.
				continue
			}
			resName := target
			resInstance := resourceInstance(instance, intentManagedRepo, resName)

			r := ref{
				template:         template,
				packageInstance:  instance,
				instance:         resInstance,
				repository:       krcEnvRepoName,
				unitType:         v1.UnitTypeResource,
				domain:           v1.DomainKRC,
				resourceTemplate: intentManagedRepo,
				resourceName:     resName,
				entry:            entry,
				unit:             unit,
				values:           map[string]any{},
			}
			rp, phs, err := resolveOne(r, priorByInstance[resInstance], forceMode)
			if err != nil {
				return nil, fmt.Errorf("spec.controllers.kubernetesResources (%s): %w", target, err)
			}
			rp.Controller = &v1.ControllerBinding{
				Kind:     v1.RoleKRC,
				Instance: instance,
				Intent:   intentManagedRepo,
			}
			fp.Spec.Packages = append(fp.Spec.Packages, rp)
			allPhs = append(allPhs, phs...)
		}
	}
	return allPhs, nil
}

// findControllerPackage resolves spec.controllers.{kubernetes,service}Resources
// to a catalog entry. Same shape as findProvider (bindings) but surfaces a
// clearer error when the repo or instance cannot be located.
func findControllerPackage(p *v1.Provision, cat *catalog.Catalog, repoName, instance string) (catalog.Entry, string, string, error) {
	for _, r := range p.Spec.Repositories {
		if r.Type != "k8s-gitops-generic" || r.Name != repoName {
			continue
		}
		for _, pr := range r.Packages {
			entry, resolvedInstance, err := packageEntry(pr, cat)
			if err != nil {
				continue
			}
			if resolvedInstance == instance {
				return entry, pr.Template, resolvedInstance, nil
			}
		}
		return catalog.Entry{}, "", "", fmt.Errorf("instance %q not selected in generic repo %q", instance, repoName)
	}
	return catalog.Entry{}, "", "", fmt.Errorf("generic repo %q not declared in Provision", repoName)
}
