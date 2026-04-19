// Bindings phase: materializes capability bindings declared on consumer
// PackageRefs into a consumer-side ResolvedPackage and a provider-side
// ResolvedPackage per env repo that references the provider's generic repo.
//
// This runs after the main resolve loop so provider install ResolvedValues
// are available when we compute `input:<key>` exports. Resolved binding
// units reuse `resolveOne` so descriptor defaults, placeholder scanning,
// and prior-fill preservation all behave identically to ordinary resources.

package resolve

import (
	"fmt"
	"sort"
	"strings"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
	"github.com/crmarques/gitups/internal/catalog"
)

func resolveBindings(
	p *v1.Provision,
	cat *catalog.Catalog,
	envKey string,
	fp *v1.FullProvision,
	priorByInstance map[string]*v1.ResolvedPackage,
	forceMode bool,
) ([]v1.Placeholder, error) {
	installByInstance := map[string]*v1.ResolvedPackage{}
	for i := range fp.Spec.Packages {
		rp := &fp.Spec.Packages[i]
		if rp.UnitType == v1.UnitTypeInstall {
			installByInstance[rp.Instance] = rp
		}
	}

	envReposByRef := map[string][]v1.RepositoryDecl{}
	for _, r := range p.Spec.Repositories {
		if r.Type == "k8s-gitops-env" && r.RepoRef != nil {
			envReposByRef[r.RepoRef.Name] = append(envReposByRef[r.RepoRef.Name], r)
		}
	}

	seenBinding := map[string]bool{}
	var allPhs []v1.Placeholder

	for ri, repo := range p.Spec.Repositories {
		consumerRepoName := substituteEnv(repo.Name, envKey)
		for pi, pr := range repo.Packages {
			if len(pr.Bindings) == 0 {
				continue
			}
			consumerEntry, consumerInstance, err := packageEntry(pr, cat)
			if err != nil {
				return nil, fmt.Errorf("spec.repositories[%d].packages[%d]: %w", ri, pi, err)
			}

			for bi, b := range pr.Bindings {
				loc := fmt.Sprintf("spec.repositories[%d].packages[%d].bindings[%d]", ri, pi, bi)
				if err := validateBinding(b, loc, seenBinding); err != nil {
					return nil, err
				}
				seenBinding[b.Name] = true

				req := lookupRequire(consumerEntry.Def, b.Capability)
				if req == nil {
					return nil, fmt.Errorf("%s (%s): consumer package %q does not declare requires for capability %q",
						loc, b.Name, consumerEntry.Def.Metadata.Name, b.Capability)
				}
				consumerUnit, ok := consumerEntry.Resources()[req.ResourceTemplate]
				if !ok {
					return nil, fmt.Errorf("%s (%s): consumer resourceTemplate %q not found in package %q",
						loc, b.Name, req.ResourceTemplate, consumerEntry.Def.Metadata.Name)
				}

				providerEntry, providerTemplate, providerInstance, err := findProvider(p, cat, b.Provider.Repo, b.Provider.Instance)
				if err != nil {
					return nil, fmt.Errorf("%s (%s): %w", loc, b.Name, err)
				}
				prov := lookupProvide(providerEntry.Def, b.Capability)
				if prov == nil {
					return nil, fmt.Errorf("%s (%s): provider package %q does not declare provides for capability %q",
						loc, b.Name, providerEntry.Def.Metadata.Name, b.Capability)
				}
				providerUnit, ok := providerEntry.Resources()[prov.ResourceTemplate]
				if !ok {
					return nil, fmt.Errorf("%s (%s): provider resourceTemplate %q not found in package %q",
						loc, b.Name, prov.ResourceTemplate, providerEntry.Def.Metadata.Name)
				}

				exportByName := map[string]v1.CapabilityExport{}
				for _, e := range prov.Exports {
					exportByName[e.Name] = e
				}
				for _, c := range req.Consumes {
					exportName := strings.TrimPrefix(c.From, "provider.")
					if _, ok := exportByName[exportName]; !ok {
						return nil, fmt.Errorf("%s (%s): consumes input %q references unknown export %q on provider %q",
							loc, b.Name, c.Input, exportName, providerEntry.Def.Metadata.Name)
					}
				}
				for _, e := range prov.Exports {
					if strings.HasPrefix(e.From, "binding:") {
						key := strings.TrimPrefix(e.From, "binding:")
						if _, ok := b.Values[key]; !ok {
							return nil, fmt.Errorf("%s (%s) export %q: binding values must supply key %q",
								loc, b.Name, e.Name, key)
						}
					}
				}

				envRepos := append([]v1.RepositoryDecl(nil), envReposByRef[b.Provider.Repo]...)
				if len(envRepos) == 0 {
					return nil, fmt.Errorf("%s (%s): no env repository references provider generic repo %q; cannot place provider-side resource",
						loc, b.Name, b.Provider.Repo)
				}
				sort.SliceStable(envRepos, func(i, j int) bool {
					return substituteEnv(envRepos[i].Name, envKey) < substituteEnv(envRepos[j].Name, envKey)
				})

				providerInstallRP := installByInstance[providerInstance]
				if providerInstallRP == nil {
					return nil, fmt.Errorf("%s (%s): provider %q install was not resolved (provider package not selected?)",
						loc, b.Name, providerInstance)
				}

				consumerTemplate := fullyQualifiedTemplate(p, consumerEntry, repo.Name, pr.Template)

				for _, envRepo := range envRepos {
					providerEnvRepoName := substituteEnv(envRepo.Name, envKey)
					suffix := providerEnvRepoName
					consumerResName := b.Name + "-" + suffix
					providerResName := b.Name + "-" + suffix
					consumerInst := resourceInstance(consumerInstance, req.ResourceTemplate, consumerResName)
					providerInst := resourceInstance(providerInstance, prov.ResourceTemplate, providerResName)

					exportValues, err := computeExportValues(
						prov.Exports, b.Values, providerInstallRP.ResolvedValues,
						priorByInstance[consumerInst], priorByInstance[providerInst],
						req.Consumes,
					)
					if err != nil {
						return nil, fmt.Errorf("%s (%s): %w", loc, b.Name, err)
					}

					consumerValues, consumerReasons, consumerSensitive, err := buildConsumerValues(req, prov, exportValues, b, providerInstance, providerEnvRepoName)
					if err != nil {
						return nil, fmt.Errorf("%s (%s): %w", loc, b.Name, err)
					}
					providerValues, providerReasons, providerSensitive, err := buildProviderValues(prov, exportValues, b, consumerInstance, consumerRepoName)
					if err != nil {
						return nil, fmt.Errorf("%s (%s): %w", loc, b.Name, err)
					}

					providerDepAlias := providerInstance + "/resources/" + prov.ResourceTemplate + "/" + providerResName

					consumerRef := ref{
						template:         consumerTemplate,
						packageInstance:  consumerInstance,
						instance:         consumerInst,
						repository:       consumerRepoName,
						unitType:         v1.UnitTypeResource,
						domain:           v1.DomainResources,
						resourceTemplate: req.ResourceTemplate,
						resourceName:     consumerResName,
						entry:            consumerEntry,
						unit:             consumerUnit,
						values:           consumerValues,
						extraReasons:     consumerReasons,
						extraSensitive:   consumerSensitive,
						binding:          &v1.BindingOrigin{Name: b.Name, Capability: b.Capability, Side: "consumer"},
						extraDependsOn:   []string{providerDepAlias},
					}
					rpConsumer, consumerPhs, err := resolveOne(consumerRef, priorByInstance[consumerInst], forceMode)
					if err != nil {
						return nil, fmt.Errorf("%s (%s) consumer: %w", loc, b.Name, err)
					}

					providerRef := ref{
						template:         providerTemplate,
						packageInstance:  providerInstance,
						instance:         providerInst,
						repository:       providerEnvRepoName,
						unitType:         v1.UnitTypeResource,
						domain:           v1.DomainResources,
						resourceTemplate: prov.ResourceTemplate,
						resourceName:     providerResName,
						entry:            providerEntry,
						unit:             providerUnit,
						values:           providerValues,
						extraReasons:     providerReasons,
						extraSensitive:   providerSensitive,
						binding:          &v1.BindingOrigin{Name: b.Name, Capability: b.Capability, Side: "provider"},
					}
					rpProvider, providerPhs, err := resolveOne(providerRef, priorByInstance[providerInst], forceMode)
					if err != nil {
						return nil, fmt.Errorf("%s (%s) provider: %w", loc, b.Name, err)
					}

					fp.Spec.Packages = append(fp.Spec.Packages, rpConsumer, rpProvider)
					allPhs = append(allPhs, consumerPhs...)
					allPhs = append(allPhs, providerPhs...)
				}
			}
		}
	}

	return allPhs, nil
}

func validateBinding(b v1.BindingRef, loc string, seenBinding map[string]bool) error {
	if b.Name == "" {
		return fmt.Errorf("%s: name is required", loc)
	}
	if seenBinding[b.Name] {
		return fmt.Errorf("%s (%s): duplicate binding name across Provision", loc, b.Name)
	}
	if b.Capability == "" {
		return fmt.Errorf("%s (%s): capability is required", loc, b.Name)
	}
	if b.Provider.Repo == "" || b.Provider.Instance == "" {
		return fmt.Errorf("%s (%s): provider.repo and provider.instance are required", loc, b.Name)
	}
	return nil
}

func lookupProvide(def *v1.PackageDefinition, capability string) *v1.CapabilityProvide {
	for i := range def.Spec.Provides {
		if def.Spec.Provides[i].Capability == capability {
			return &def.Spec.Provides[i]
		}
	}
	return nil
}

func lookupRequire(def *v1.PackageDefinition, capability string) *v1.CapabilityRequire {
	for i := range def.Spec.Requires {
		if def.Spec.Requires[i].Capability == capability {
			return &def.Spec.Requires[i]
		}
	}
	return nil
}

// findProvider locates the PackageRef in the named generic repo whose
// resolved instance matches and returns its catalog entry, template, and
// resolved instance name.
func findProvider(p *v1.Provision, cat *catalog.Catalog, repoName, instance string) (catalog.Entry, string, string, error) {
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
		return catalog.Entry{}, "", "", fmt.Errorf("provider instance %q not found in generic repo %q", instance, repoName)
	}
	return catalog.Entry{}, "", "", fmt.Errorf("generic repo %q not declared in Provision", repoName)
}

// fullyQualifiedTemplate returns the consumer's template string as written
// in its PackageRef. We use the declared form so ResolvedPackage.Template
// matches what the user wrote.
func fullyQualifiedTemplate(p *v1.Provision, entry catalog.Entry, repoName, declared string) string {
	if declared != "" {
		return declared
	}
	return entry.Source + "/" + entry.Def.Metadata.Name
}

// computeExportValues resolves each export to a concrete value (or sentinel)
// for one binding × env pair. Secret exports sync across both sides by
// preferring any non-sentinel prior fill on either side.
func computeExportValues(
	exports []v1.CapabilityExport,
	bindingValues map[string]any,
	providerInstallValues map[string]any,
	priorConsumer, priorProvider *v1.ResolvedPackage,
	consumes []v1.CapabilityConsume,
) (map[string]any, error) {
	out := map[string]any{}
	consumerInputByExport := map[string]string{}
	for _, c := range consumes {
		consumerInputByExport[strings.TrimPrefix(c.From, "provider.")] = c.Input
	}
	for _, e := range exports {
		val, err := resolveExportValue(e, providerInstallValues, bindingValues)
		if err != nil {
			return nil, fmt.Errorf("export %q: %w", e.Name, err)
		}
		if e.Secret {
			if fill := nonSentinelFill(priorProvider, e.Name); fill != nil {
				val = cloneValue(fill)
			} else if consumerKey, ok := consumerInputByExport[e.Name]; ok {
				if fill := nonSentinelFill(priorConsumer, consumerKey); fill != nil {
					val = cloneValue(fill)
				}
			}
		}
		out[e.Name] = val
	}
	return out, nil
}

func nonSentinelFill(rp *v1.ResolvedPackage, path string) any {
	if rp == nil {
		return nil
	}
	val := lookupByPath(rp.ResolvedValues, path)
	if val == nil {
		return nil
	}
	if isSentinelLeaf(val) {
		return nil
	}
	return val
}

func lookupByPath(m map[string]any, path string) any {
	if m == nil {
		return nil
	}
	parts := strings.Split(path, ".")
	var cur any = m
	for _, p := range parts {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = mm[p]
		if !ok {
			return nil
		}
	}
	return cur
}

func resolveExportValue(e v1.CapabilityExport, providerInstallValues, bindingValues map[string]any) (any, error) {
	switch {
	case e.From == "placeholder":
		return v1.PlaceholderSentinel, nil
	case strings.HasPrefix(e.From, "input:"):
		key := strings.TrimPrefix(e.From, "input:")
		val := lookupByPath(providerInstallValues, key)
		if val == nil {
			return nil, fmt.Errorf("provider has no resolved input %q", key)
		}
		return cloneValue(val), nil
	case strings.HasPrefix(e.From, "binding:"):
		key := strings.TrimPrefix(e.From, "binding:")
		val, ok := bindingValues[key]
		if !ok {
			return nil, fmt.Errorf("binding values missing key %q", key)
		}
		return cloneValue(val), nil
	default:
		return nil, fmt.Errorf("unknown from grammar %q", e.From)
	}
}

func buildConsumerValues(
	req *v1.CapabilityRequire,
	prov *v1.CapabilityProvide,
	exportValues map[string]any,
	b v1.BindingRef,
	providerInstance, providerEnvRepoName string,
) (map[string]any, map[string]string, map[string]bool, error) {
	exportByName := map[string]v1.CapabilityExport{}
	for _, e := range prov.Exports {
		exportByName[e.Name] = e
	}
	values := map[string]any{}
	reasons := map[string]string{}
	sensitive := map[string]bool{}
	for _, c := range req.Consumes {
		exportName := strings.TrimPrefix(c.From, "provider.")
		val := exportValues[exportName]
		if err := setByPath(values, c.Input, val); err != nil {
			return nil, nil, nil, fmt.Errorf("consumes %q: %w", c.Input, err)
		}
		if exportByName[exportName].Secret {
			reasons[c.Input] = fmt.Sprintf("binding %q (capability %q): secret export %q sourced from provider %s in %s",
				b.Name, b.Capability, exportName, providerInstance, providerEnvRepoName)
			sensitive[c.Input] = true
		}
	}
	return values, reasons, sensitive, nil
}

func buildProviderValues(
	prov *v1.CapabilityProvide,
	exportValues map[string]any,
	b v1.BindingRef,
	consumerInstance, consumerRepoName string,
) (map[string]any, map[string]string, map[string]bool, error) {
	values := map[string]any{}
	reasons := map[string]string{}
	sensitive := map[string]bool{}
	for _, e := range prov.Exports {
		val := exportValues[e.Name]
		if err := setByPath(values, e.Name, val); err != nil {
			return nil, nil, nil, fmt.Errorf("export %q: %w", e.Name, err)
		}
		if e.Secret {
			reasons[e.Name] = fmt.Sprintf("binding %q (capability %q): secret export %q offered to consumer %s in %s",
				b.Name, b.Capability, e.Name, consumerInstance, consumerRepoName)
			sensitive[e.Name] = true
		}
	}
	return values, reasons, sensitive, nil
}
