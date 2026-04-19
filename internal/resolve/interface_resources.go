// Interface-resource projection ("Phase 5C"): materialises one
// ResolvedPackage per Provision.spec.repositories[].interfaceResources[]
// entry into its service-resources repo, tagged with a Controller that
// points at the selected SRC. The CR body reuses the provider package's
// resources/<resourceTemplate>/ descriptor so gitups core does not hold
// any interface-specific shape.

package resolve

import (
	"fmt"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
	"github.com/crmarques/gitups/internal/catalog"
)

func resolveInterfaceResources(
	p *v1.Provision,
	cat *catalog.Catalog,
	envKey string,
	fp *v1.FullProvision,
	priorByInstance map[string]*v1.ResolvedPackage,
	forceMode bool,
) ([]v1.Placeholder, error) {
	// Short-circuit: no service-resources repos, nothing to project.
	hasAny := false
	for _, r := range p.Spec.Repositories {
		if r.Type == v1.RepoTypeServiceResources && len(r.InterfaceResources) > 0 {
			hasAny = true
			break
		}
	}
	if !hasAny {
		return nil, nil
	}

	if p.Spec.Controllers == nil || p.Spec.Controllers.ServiceResources == nil {
		return nil, fmt.Errorf("spec.controllers.serviceResources is required when any service-resources repo carries interfaceResources[]")
	}
	srcEntry, srcTemplate, srcInstance, err := findControllerPackage(
		p, cat, p.Spec.Controllers.ServiceResources.Repo, p.Spec.Controllers.ServiceResources.Instance)
	if err != nil {
		return nil, fmt.Errorf("spec.controllers.serviceResources: %w", err)
	}
	if srcEntry.Def.Spec.Role != v1.RoleSRC {
		return nil, fmt.Errorf("spec.controllers.serviceResources: package %q has role %q; expected %q",
			srcEntry.Def.Metadata.Name, srcEntry.Def.Spec.Role, v1.RoleSRC)
	}
	// Index the SRC's bundles so we can fail fast when a projected
	// interface has no matching reconciler bundle declared.
	srcBundles := map[string]bool{}
	for _, b := range srcEntry.Def.Spec.Bundles {
		srcBundles[b.Interface+"/"+b.Version] = true
	}

	var allPhs []v1.Placeholder
	for _, repo := range p.Spec.Repositories {
		if repo.Type != v1.RepoTypeServiceResources || len(repo.InterfaceResources) == 0 {
			continue
		}
		providerEntry, providerTemplate, providerInstance, perr := findWorkloadPackage(p, cat, repo.ServiceRef.Repo, repo.ServiceRef.Instance)
		if perr != nil {
			return nil, fmt.Errorf("spec.repositories (%s).serviceRef: %w", repo.Name, perr)
		}
		repoName := substituteEnv(repo.Name, envKey)

		for j, ir := range repo.InterfaceResources {
			prefix := fmt.Sprintf("spec.repositories (%s).interfaceResources[%d]", repo.Name, j)
			ifKey := ir.Interface + "/" + ir.Version
			if !srcBundles[ifKey] {
				return nil, fmt.Errorf("%s: SRC %q declares no spec.bundles entry for %s — the SRC cannot reconcile this CR at runtime",
					prefix, srcEntry.Def.Metadata.Name, ifKey)
			}
			tmpl, err := providerResourceTemplate(providerEntry, ir)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", prefix, err)
			}
			unit, ok := providerEntry.Resources()[tmpl]
			if !ok {
				return nil, fmt.Errorf("%s: provider package %q has no resources/%s/ template (required for interface %s)",
					prefix, providerEntry.Def.Metadata.Name, tmpl, ifKey)
			}
			resInstance := projectedInstance(repoName, ir.Name)
			r := ref{
				template:         providerTemplate,
				packageInstance:  providerInstance,
				instance:         resInstance,
				repository:       repoName,
				unitType:         v1.UnitTypeResource,
				domain:           v1.DomainResources,
				resourceTemplate: tmpl,
				resourceName:     ir.Name,
				entry:            providerEntry,
				unit:             unit,
				values:           cloneMap(ir.Values),
				binding: &v1.BindingOrigin{
					Name:       ir.Name,
					Capability: ifKey,
					Side:       "consumer",
				},
			}
			rp, phs, err := resolveOne(r, priorByInstance[resInstance], forceMode)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", prefix, err)
			}
			rp.Controller = &v1.ControllerBinding{
				Kind:     v1.RoleSRC,
				Instance: srcInstance,
				Template: srcTemplate,
				Intent:   intentManagedResource,
			}
			fp.Spec.Packages = append(fp.Spec.Packages, rp)
			allPhs = append(allPhs, phs...)
		}
	}
	return allPhs, nil
}

// providerResourceTemplate returns the resources/<template>/ name the
// resolver should render. Precedence: explicit override on the
// InterfaceResourceDecl > the provider's implements[] entry default >
// a conventional "<interface>-<version>" fallback. The fallback lets
// simple packages declare `implements[]` without a separate
// resourceTemplate field when the matching directory name follows the
// convention.
func providerResourceTemplate(entry catalog.Entry, ir v1.InterfaceResourceDecl) (string, error) {
	if ir.ResourceTemplate != "" {
		return ir.ResourceTemplate, nil
	}
	for _, impl := range entry.Def.Spec.Implements {
		if impl.Interface == ir.Interface && impl.Version == ir.Version {
			if impl.ResourceTemplate != "" {
				return impl.ResourceTemplate, nil
			}
			break
		}
	}
	// Look for an entry declaring the interface at all; if none, the
	// provider isn't actually a valid implementer.
	found := false
	for _, impl := range entry.Def.Spec.Implements {
		if impl.Interface == ir.Interface && impl.Version == ir.Version {
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("provider package %q does not declare spec.implements for %s/%s",
			entry.Def.Metadata.Name, ir.Interface, ir.Version)
	}
	return ir.Interface + "-" + ir.Version, nil
}

// findWorkloadPackage locates the provider (serviceRef target) in a
// generic kubernetes-resources repo. Shape parallel to findControllerPackage
// but returns the catalog entry even for RoleWorkload so we can read its
// resources/<template>/ descriptors.
func findWorkloadPackage(p *v1.Provision, cat *catalog.Catalog, repoName, instance string) (catalog.Entry, string, string, error) {
	for _, r := range p.Spec.Repositories {
		if r.Type != v1.RepoTypeKubernetesResources || r.RepoRef != nil || r.Name != repoName {
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

// projectedInstance builds a unique instance name for a projected
// interface resource. Includes the target repo so two service-resources
// repos (e.g. dev + prod) can both carry a "gitea" backend without
// colliding.
func projectedInstance(repoName, entryName string) string {
	return repoName + "-" + entryName
}

// intentManagedResource is the SRC intent name used for direct CR
// reconciliation (as opposed to managed-script, which wraps a shell
// script in a Job). SRCs opt in by providing a
// service-resource-controller/managed-resource/ directory.
const intentManagedResource = "managed-resource"
