// Package resolve implements stage 1 of gitups: expand a Provision into a
// FullProvision. It merges descriptor defaults with user overrides,
// topologically sorts by dependsOn, scans for unfilled placeholders, and
// preserves user edits on re-expand.
package resolve

import (
	"fmt"
	"sort"
	"strings"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
	"github.com/crmarques/gitups/internal/catalog"
	"github.com/crmarques/gitups/internal/placeholders"
)

// Options controls Expand behavior.
type Options struct {
	// Prior is an optional previously-generated FullProvision whose user edits
	// should be preserved on re-expand. Nil on first run.
	Prior *v1.FullProvision
	// Force re-derives defaults from the catalog, but still preserves
	// user-authored fills at paths that are placeholders in the fresh expansion.
	Force bool
	// OutputPath seeds spec.repository.outputPath. If Prior is set, its value
	// wins unless empty.
	OutputPath string
	// ExtendedFrom is written advisorily into FullProvision.spec.
	ExtendedFrom *v1.ExtendedFrom
}

// Expand produces a FullProvision from a Provision and a loaded Catalog.
func Expand(p *v1.Provision, cat *catalog.Catalog, opts Options) (*v1.FullProvision, error) {
	envKey := p.Spec.EnvKey
	if envKey == "" {
		envKey = p.Metadata.Name
	}

	refs, repos, err := collectRefs(p, cat, envKey)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("spec.repositories produced no install or resource units")
	}

	ordered, err := topoSort(refs)
	if err != nil {
		return nil, err
	}

	fp := &v1.FullProvision{
		APIVersion: v1.APIVersion,
		Kind:       v1.KindFullProvision,
		Metadata:   v1.ObjectMeta{Name: p.Metadata.Name},
		Spec: v1.FullProvisionSpec{
			SourceProvisionRef: v1.ObjectMeta{Name: p.Metadata.Name},
			ExtendedFrom:       opts.ExtendedFrom,
			Sources:            p.Spec.Sources,
			Repository:         v1.RepositoryBlock{Layout: "split", OutputPath: opts.OutputPath},
			Repositories:       repos,
		},
	}
	if opts.Prior != nil && !opts.Force {
		if opts.Prior.Spec.Repository.Layout != "" {
			fp.Spec.Repository.Layout = opts.Prior.Spec.Repository.Layout
		}
		if opts.Prior.Spec.Repository.OutputPath != "" {
			fp.Spec.Repository.OutputPath = opts.Prior.Spec.Repository.OutputPath
		}
	}
	if fp.Spec.Repository.OutputPath == "" {
		fp.Spec.Repository.OutputPath = "./out/" + p.Metadata.Name
	}

	priorByInstance := map[string]*v1.ResolvedPackage{}
	if opts.Prior != nil {
		for i := range opts.Prior.Spec.Packages {
			rp := &opts.Prior.Spec.Packages[i]
			priorByInstance[rp.Instance] = rp
		}
	}

	var placeholderList []v1.Placeholder
	for _, r := range ordered {
		rp, phs, err := resolveOne(r, priorByInstance[r.instance], opts.Force)
		if err != nil {
			return nil, err
		}
		fp.Spec.Packages = append(fp.Spec.Packages, rp)
		placeholderList = append(placeholderList, phs...)
	}

	bindingPhs, err := resolveBindings(p, cat, envKey, fp, priorByInstance, opts.Force)
	if err != nil {
		return nil, err
	}
	placeholderList = append(placeholderList, bindingPhs...)

	controllerPhs, err := resolveControllers(p, cat, envKey, fp, priorByInstance, opts.Force)
	if err != nil {
		return nil, err
	}
	placeholderList = append(placeholderList, controllerPhs...)

	if err := computeApplyWaves(fp); err != nil {
		return nil, err
	}

	sort.SliceStable(placeholderList, func(i, j int) bool { return placeholderList[i].Path < placeholderList[j].Path })
	fp.Spec.Placeholders = placeholderList
	return fp, nil
}

type ref struct {
	template         string
	packageInstance  string
	instance         string
	repository       string
	unitType         string
	domain           string
	installMethod    string
	resourceTemplate string
	resourceName     string
	entry            catalog.Entry
	unit             catalog.Unit
	values           map[string]any
	roleOverride     v1.Role
	// binding-only: extra reason text applied to placeholders at the given
	// input-key paths, sensitivity flag overrides, a ResolvedPackage.Binding
	// origin marker, and extra dependsOn strings to append.
	extraReasons   map[string]string
	extraSensitive map[string]bool
	binding        *v1.BindingOrigin
	extraDependsOn []string
}

func collectRefs(p *v1.Provision, cat *catalog.Catalog, envKey string) ([]ref, []v1.ResolvedRepository, error) {
	// Generic-style kubernetes-resources repos are those with no
	// repoRef. Indexed by name so env repos can derive a default
	// package list when the user leaves packages[] empty.
	genericByName := map[string]v1.RepositoryDecl{}
	for _, repo := range p.Spec.Repositories {
		if repo.Type == v1.RepoTypeKubernetesResources && repo.RepoRef == nil {
			genericByName[repo.Name] = repo
		}
	}

	seenInstance := map[string]bool{}
	var out []ref
	resolvedRepos := make([]v1.ResolvedRepository, 0, len(p.Spec.Repositories))
	for ri, repo := range p.Spec.Repositories {
		repoName := substituteEnv(repo.Name, envKey)
		msr := cloneManagedServiceRef(repo.ManagedServiceRef)
		if msr != nil {
			msr.Repo = substituteEnv(msr.Repo, envKey)
		}
		resolvedRepos = append(resolvedRepos, v1.ResolvedRepository{
			Name:              repoName,
			Description:       repo.Description,
			Type:              repo.Type,
			RepoRef:           cloneRepoRef(repo.RepoRef),
			ManagedServiceRef: msr,
		})
		// Service-resources repos carry no install/resource units —
		// they are declarest payload skeletons the user edits
		// directly. The renderer emits a README + empty kustomization.
		if repo.Type == v1.RepoTypeServiceResources {
			continue
		}
		if repo.RepoRef == nil {
			// Generic: collects install refs from packages[].
			for pi, pr := range repo.Packages {
				r, err := installRef(repoName, pr, cat)
				if err != nil {
					return nil, nil, fmt.Errorf("spec.repositories[%d].packages[%d]: %w", ri, pi, err)
				}
				if seenInstance[r.instance] {
					return nil, nil, fmt.Errorf("spec.repositories[%d].packages[%d]: duplicate instance %q", ri, pi, r.instance)
				}
				seenInstance[r.instance] = true
				out = append(out, r)
			}
			continue
		}
		// Env: collects resource refs. Inherits packages[] from the
		// referenced generic repo when the env repo leaves packages[]
		// empty.
		packages := repo.Packages
		if len(packages) == 0 {
			base, ok := genericByName[repo.RepoRef.Name]
			if !ok {
				return nil, nil, fmt.Errorf("spec.repositories[%d]: repoRef.name %q does not match a generic repository", ri, repo.RepoRef.Name)
			}
			packages = base.Packages
		}
		for pi, pr := range packages {
			rs, err := resourceRefs(repoName, pr, cat)
			if err != nil {
				return nil, nil, fmt.Errorf("spec.repositories[%d].packages[%d]: %w", ri, pi, err)
			}
			for _, r := range rs {
				if seenInstance[r.instance] {
					return nil, nil, fmt.Errorf("spec.repositories[%d].packages[%d]: duplicate instance %q", ri, pi, r.instance)
				}
				seenInstance[r.instance] = true
				out = append(out, r)
			}
		}
	}
	return out, resolvedRepos, nil
}

func installRef(repoName string, pr v1.PackageRef, cat *catalog.Catalog) (ref, error) {
	entry, packageInstance, err := packageEntry(pr, cat)
	if err != nil {
		return ref{}, err
	}
	method := pr.InstallMethod
	if method == "" {
		method = entry.Def.Spec.DefaultInstall
	}
	unit, ok := entry.Installs()[method]
	if !ok {
		return ref{}, fmt.Errorf("installMethod %q not supported by %q", method, pr.Template)
	}
	return ref{
		template:        pr.Template,
		packageInstance: packageInstance,
		instance:        packageInstance,
		repository:      repoName,
		unitType:        v1.UnitTypeInstall,
		domain:          v1.DomainInstall,
		installMethod:   method,
		entry:           entry,
		unit:            unit,
		values:          cloneMap(pr.Values),
		roleOverride:    pr.Role,
	}, nil
}

func resourceRefs(repoName string, pr v1.PackageRef, cat *catalog.Catalog) ([]ref, error) {
	entry, packageInstance, err := packageEntry(pr, cat)
	if err != nil {
		return nil, err
	}
	resources := pr.Resources
	if len(resources) == 0 {
		resources = entry.Def.Spec.DefaultResources
	}
	out := make([]ref, 0, len(resources))
	for _, rr := range resources {
		unit, ok := entry.Resources()[rr.Template]
		if !ok {
			return nil, fmt.Errorf("resource template %q not supported by %q", rr.Template, pr.Template)
		}
		values := deepMerge(pr.Values, rr.Values)
		out = append(out, ref{
			template:         pr.Template,
			packageInstance:  packageInstance,
			instance:         resourceInstance(packageInstance, rr.Template, rr.Name),
			repository:       repoName,
			unitType:         v1.UnitTypeResource,
			domain:           v1.DomainResources,
			resourceTemplate: rr.Template,
			resourceName:     rr.Name,
			entry:            entry,
			unit:             unit,
			values:           values,
			roleOverride:     pr.Role,
		})
	}
	return out, nil
}

func packageEntry(pr v1.PackageRef, cat *catalog.Catalog) (catalog.Entry, string, error) {
	entry, ok := cat.Lookup(pr.Template)
	if !ok {
		return catalog.Entry{}, "", fmt.Errorf("unknown template %q (known: %v)", pr.Template, cat.Qualified())
	}
	instance := pr.Instance
	if instance == "" {
		parts := strings.Split(pr.Template, "/")
		instance = parts[len(parts)-1]
	}
	return entry, instance, nil
}

func resourceInstance(packageInstance, template, name string) string {
	return packageInstance + "-" + template + "-" + name
}

// renderedDirFor returns the rendered sub-path for a ref, derived from its
// domain + unit type. Install units render under install/<method>; every
// other domain renders under <domain>/<sub-name>/<resource-name> where the
// sub-name is the resource template (resources/) or intent name (KRC/SRC).
func renderedDirFor(r ref) string {
	switch r.unitType {
	case v1.UnitTypeInstall:
		return "packages/" + r.packageInstance + "/" + v1.DomainInstall + "/" + r.installMethod
	case v1.UnitTypeResource:
		return "packages/" + r.packageInstance + "/" + r.domain + "/" + r.resourceTemplate + "/" + r.resourceName
	}
	return ""
}

func cloneRepoRef(in *v1.RepositoryRef) *v1.RepositoryRef {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneManagedServiceRef(in *v1.ManagedServiceRef) *v1.ManagedServiceRef {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

// topoSort orders refs so that every dependency precedes its dependent.
func topoSort(refs []ref) ([]ref, error) {
	byName := map[string][]int{}
	for i, r := range refs {
		pkgName := r.entry.Def.Metadata.Name
		addRefAlias(byName, r.instance, i)
		switch r.unitType {
		case v1.UnitTypeInstall:
			addRefAlias(byName, pkgName+"/install", i)
			addRefAlias(byName, r.packageInstance+"/install", i)
			addRefAlias(byName, pkgName+"/install/"+r.installMethod, i)
			addRefAlias(byName, r.packageInstance+"/install/"+r.installMethod, i)
		case v1.UnitTypeResource:
			domain := r.domain
			if domain == "" {
				domain = v1.DomainResources
			}
			addRefAlias(byName, pkgName+"/"+domain+"/"+r.resourceTemplate, i)
			addRefAlias(byName, r.packageInstance+"/"+domain+"/"+r.resourceTemplate, i)
			addRefAlias(byName, pkgName+"/"+domain+"/"+r.resourceTemplate+"/"+r.resourceName, i)
			addRefAlias(byName, r.packageInstance+"/"+domain+"/"+r.resourceTemplate+"/"+r.resourceName, i)
		}
	}

	n := len(refs)
	indeg := make([]int, n)
	adj := make([][]int, n)
	for i, r := range refs {
		for _, dep := range r.unit.Descriptor.DependsOn {
			sources, ok := byName[dep]
			if !ok {
				continue
			}
			for _, src := range sources {
				if src == i {
					continue
				}
				adj[src] = append(adj[src], i)
				indeg[i]++
			}
		}
	}

	var ready []int
	for i := 0; i < n; i++ {
		if indeg[i] == 0 {
			ready = append(ready, i)
		}
	}
	sort.Ints(ready)
	out := make([]ref, 0, n)
	for len(ready) > 0 {
		i := ready[0]
		ready = ready[1:]
		out = append(out, refs[i])
		for _, j := range adj[i] {
			indeg[j]--
			if indeg[j] == 0 {
				ready = append(ready, j)
			}
		}
		sort.Ints(ready)
	}
	if len(out) != n {
		var cyc []string
		for i, d := range indeg {
			if d > 0 {
				cyc = append(cyc, refs[i].instance)
			}
		}
		return nil, fmt.Errorf("dependency cycle involving: %v", cyc)
	}
	return out, nil
}

func addRefAlias(byName map[string][]int, name string, idx int) {
	if name == "" {
		return
	}
	byName[name] = append(byName[name], idx)
}

func resolveOne(r ref, prior *v1.ResolvedPackage, forceMode bool) (v1.ResolvedPackage, []v1.Placeholder, error) {
	def := r.entry.Def
	desc := r.unit.Descriptor

	values := map[string]any{}
	reasons := map[string]string{}
	sensitive := map[string]bool{}
	generators := map[string]*v1.Generator{}
	for _, in := range desc.Inputs {
		val, hasDefault := inputDefault(in)
		if in.Placeholder || (in.Required && !hasDefault && val == nil) {
			val = v1.PlaceholderSentinel
		}
		if val != nil {
			if err := setByPath(values, in.Name, val); err != nil {
				return v1.ResolvedPackage{}, nil, fmt.Errorf("package %s: input %q: %w", def.Metadata.Name, in.Name, err)
			}
		}
		if containsSentinel(val) {
			reason := in.PlaceholderReason
			if reason == "" {
				reason = fmt.Sprintf("input %q has no default", in.Name)
			}
			reasons[in.Name] = reason
			sensitive[in.Name] = in.Sensitive
			if in.Generator != nil {
				generators[in.Name] = in.Generator
			}
		}
	}

	values = deepMerge(values, r.values)
	if prior != nil {
		if forceMode {
			values = overlayPlaceholderFills(values, prior.ResolvedValues)
		} else {
			values = overlayUserEdits(values, prior.ResolvedValues)
		}
	}

	role := def.Spec.Role
	if r.roleOverride != "" {
		role = r.roleOverride
	}
	renderedDir := renderedDirFor(r)
	for k, v := range r.extraReasons {
		reasons[k] = v
	}
	for k, v := range r.extraSensitive {
		sensitive[k] = v
	}

	dependsOn := append([]string(nil), desc.DependsOn...)
	dependsOn = append(dependsOn, r.extraDependsOn...)

	rp := v1.ResolvedPackage{
		Template:         r.template,
		PackageInstance:  r.packageInstance,
		UnitType:         r.unitType,
		Domain:           r.domain,
		InstallMethod:    r.installMethod,
		ResourceTemplate: r.resourceTemplate,
		ResourceName:     r.resourceName,
		Repository:       r.repository,
		Instance:         r.instance,
		Role:             role,
		Renderer:         desc.Renderer,
		DependsOn:        dependsOn,
		ResolvedValues:   values,
		RenderedPaths:    v1.RenderedPaths{Repo: r.repository, Dir: renderedDir},
		Binding:          r.binding,
	}

	phs := placeholders.Scan(r.instance, values, reasons, sensitive, generators)
	return rp, phs, nil
}

func substituteEnv(s, envKey string) string {
	return strings.ReplaceAll(s, "{{.Env}}", envKey)
}

func inputDefault(in v1.InputSpec) (any, bool) {
	if in.Default == nil {
		return nil, false
	}
	return in.Default, true
}

func containsSentinel(v any) bool {
	return placeholders.Contains(v)
}

func setByPath(m map[string]any, path string, v any) error {
	parts := strings.Split(path, ".")
	cur := m
	for i, p := range parts {
		if i == len(parts)-1 {
			cur[p] = v
			return nil
		}
		next, ok := cur[p]
		if !ok {
			child := map[string]any{}
			cur[p] = child
			cur = child
			continue
		}
		nm, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("path %q: segment %q is not a map", path, p)
		}
		cur = nm
	}
	return nil
}

func deepMerge(dst, src map[string]any) map[string]any {
	out := cloneMap(dst)
	for k, sv := range src {
		if dv, ok := out[k]; ok {
			dm, dmOK := dv.(map[string]any)
			sm, smOK := sv.(map[string]any)
			if dmOK && smOK {
				out[k] = deepMerge(dm, sm)
				continue
			}
		}
		out[k] = cloneValue(sv)
	}
	return out
}

func overlayPlaceholderFills(base, prior map[string]any) map[string]any {
	out := cloneMap(base)
	for k, pv := range prior {
		bv, exists := out[k]
		if !exists {
			continue
		}
		if pm, ok := pv.(map[string]any); ok {
			if bm, ok := bv.(map[string]any); ok {
				out[k] = overlayPlaceholderFills(bm, pm)
			}
			continue
		}
		if isSentinelLeaf(bv) && !isSentinelLeaf(pv) {
			out[k] = cloneValue(pv)
		}
	}
	return out
}

func overlayUserEdits(base, prior map[string]any) map[string]any {
	out := cloneMap(base)
	for k, pv := range prior {
		bv, exists := out[k]
		if pm, ok := pv.(map[string]any); ok {
			if bm, ok := bv.(map[string]any); ok && exists {
				out[k] = overlayUserEdits(bm, pm)
			} else {
				out[k] = cloneValue(pv)
			}
			continue
		}
		if isSentinelLeaf(pv) {
			continue
		}
		out[k] = cloneValue(pv)
	}
	return out
}

func isSentinelLeaf(v any) bool {
	if s, ok := v.(string); ok {
		return s == v1.PlaceholderSentinel
	}
	if arr, ok := v.([]any); ok {
		for _, e := range arr {
			if s, ok := e.(string); ok && s == v1.PlaceholderSentinel {
				return true
			}
		}
	}
	return false
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneMap(t)
	case []any:
		arr := make([]any, len(t))
		for i, e := range t {
			arr[i] = cloneValue(e)
		}
		return arr
	default:
		return v
	}
}
