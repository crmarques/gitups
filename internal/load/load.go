// Package load handles YAML decoding and schema-level validation of on-disk
// gitups documents (Provision, FullProvision, PackageDefinition).
package load

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
)

// DetectKind reads a file and returns its TypeMeta without full unmarshal.
func DetectKind(path string) (v1.TypeMeta, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return v1.TypeMeta{}, fmt.Errorf("read %s: %w", path, err)
	}
	var tm v1.TypeMeta
	if err := yaml.Unmarshal(raw, &tm); err != nil {
		return v1.TypeMeta{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return tm, nil
}

// Provision loads and validates a Provision file as it lies on disk, without
// resolving spec.extends. Callers that want the merged effective Provision
// should use ProvisionResolved.
func Provision(path string) (*v1.Provision, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := rejectOldProvisionFields(raw); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	var p v1.Provision
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := ValidateProvision(&p); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	return &p, nil
}

// ProvisionResolved loads a Provision and, if it declares spec.extends, merges
// it with its base Provision so expand/render see a single effective object.
// The returned Provision never carries spec.extends; the advisory trace is
// returned separately for persistence in FullProvision.
//
// Rules:
//   - Only one level of extends is allowed (base must not itself have extends).
//   - sources merge by appending base entries not present in env; duplicate
//     source names error.
//   - repositories merge by name, with env entries replacing base entries.
//   - metadata and spec.envKey always come from the env Provision.
func ProvisionResolved(path string) (*v1.Provision, *v1.ExtendedFrom, error) {
	env, err := Provision(path)
	if err != nil {
		return nil, nil, err
	}
	if env.Spec.Extends == nil {
		return env, nil, nil
	}
	ext := env.Spec.Extends
	if ext.Source == "" {
		return nil, nil, fmt.Errorf("%s: spec.extends.source is required", path)
	}
	if strings.HasPrefix(ext.Source, "git+") {
		return nil, nil, fmt.Errorf("%s: spec.extends.source %q: git+ sources are not yet supported; use a filesystem path",
			path, ext.Source)
	}
	if strings.Contains(ext.Source, "://") {
		return nil, nil, fmt.Errorf("%s: spec.extends.source %q: only filesystem paths are supported today",
			path, ext.Source)
	}
	basePath := ext.Source
	if !filepath.IsAbs(basePath) {
		basePath = filepath.Join(filepath.Dir(path), basePath)
	}
	base, err := Provision(basePath)
	if err != nil {
		return nil, nil, fmt.Errorf("load base provision %s: %w", basePath, err)
	}
	if base.Spec.Extends != nil {
		return nil, nil, fmt.Errorf("%s: base provision %s also declares spec.extends; only one level of extends is supported",
			path, basePath)
	}
	merged, err := mergeProvisions(base, env)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: merge with base %s: %w", path, basePath, err)
	}
	return merged, &v1.ExtendedFrom{Source: ext.Source, Ref: ext.Ref}, nil
}

// mergeProvisions returns a new effective Provision: env metadata + envKey,
// sources = base ++ env (by name, no collisions), repositories replaced by
// name with env entries appending after inherited base entries.
func mergeProvisions(base, env *v1.Provision) (*v1.Provision, error) {
	out := &v1.Provision{
		APIVersion: env.APIVersion,
		Kind:       env.Kind,
		Metadata:   env.Metadata,
		Spec: v1.ProvisionSpec{
			EnvKey: env.Spec.EnvKey,
		},
	}

	seenSource := map[string]bool{}
	for _, s := range base.Spec.Sources {
		out.Spec.Sources = append(out.Spec.Sources, s)
		seenSource[s.Name] = true
	}
	for _, s := range env.Spec.Sources {
		if seenSource[s.Name] {
			return nil, fmt.Errorf("source %q: conflicting definitions in base and env provisions", s.Name)
		}
		seenSource[s.Name] = true
		out.Spec.Sources = append(out.Spec.Sources, s)
	}

	repoIdxByName := map[string]int{}
	for i, r := range base.Spec.Repositories {
		repoIdxByName[r.Name] = i
	}
	repos := append([]v1.RepositoryDecl(nil), base.Spec.Repositories...)
	for _, envR := range env.Spec.Repositories {
		if idx, ok := repoIdxByName[envR.Name]; ok {
			repos[idx] = envR
			continue
		}
		repos = append(repos, envR)
	}
	out.Spec.Repositories = repos
	return out, nil
}

// deepMergeMaps overlays src onto dst. Nil inputs are treated as empty. Maps
// merge recursively; other types replace. Neither input is mutated.
func deepMergeMaps(dst, src map[string]any) map[string]any {
	if dst == nil && src == nil {
		return nil
	}
	out := map[string]any{}
	for k, v := range dst {
		out[k] = cloneAny(v)
	}
	for k, sv := range src {
		if dv, ok := out[k]; ok {
			if dm, dmOK := dv.(map[string]any); dmOK {
				if sm, smOK := sv.(map[string]any); smOK {
					out[k] = deepMergeMaps(dm, sm)
					continue
				}
			}
		}
		out[k] = cloneAny(sv)
	}
	return out
}

func cloneAny(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = cloneAny(vv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = cloneAny(e)
		}
		return out
	default:
		return v
	}
}

func rejectOldProvisionFields(raw []byte) error {
	doc, err := decodeMap(raw)
	if err != nil {
		return err
	}
	spec := childMap(doc, "spec")
	if spec == nil {
		return nil
	}
	if _, ok := spec["packages"]; ok {
		return fmt.Errorf("spec.packages is no longer supported; declare packages under spec.repositories[].packages")
	}
	repos, _ := spec["repositories"].([]any)
	for i, rv := range repos {
		repo, _ := rv.(map[string]any)
		if repo == nil {
			continue
		}
		if _, ok := repo["members"]; ok {
			return fmt.Errorf("spec.repositories[%d].members is no longer supported; use packages/resources", i)
		}
		pkgs, _ := repo["packages"].([]any)
		for j, pv := range pkgs {
			pkg, _ := pv.(map[string]any)
			if pkg == nil {
				continue
			}
			for _, old := range []string{"method", "components", "component", "componentValues"} {
				if _, ok := pkg[old]; ok {
					return fmt.Errorf("spec.repositories[%d].packages[%d].%s is no longer supported", i, j, old)
				}
			}
		}
	}
	return nil
}

func rejectOldPackageFields(raw []byte) error {
	doc, err := decodeMap(raw)
	if err != nil {
		return err
	}
	spec := childMap(doc, "spec")
	for _, old := range []string{
		"inputs", "dependsOn", "installation", "components", "defaultComponents",
		"methods", "targetRepo", "renderer", "olm", "helm", "kustomize",
	} {
		if _, ok := spec[old]; ok {
			return fmt.Errorf("spec.%s belongs in install/resources descriptor.yaml or is no longer supported", old)
		}
	}
	return nil
}

func rejectOldDescriptorFields(raw []byte) error {
	doc, err := decodeMap(raw)
	if err != nil {
		return err
	}
	spec := childMap(doc, "spec")
	for _, old := range []string{
		"targetRepo", "installation", "components", "defaultComponents",
		"defaultInstall", "defaultResources", "methods",
	} {
		if _, ok := spec[old]; ok {
			return fmt.Errorf("spec.%s is not valid in descriptor.yaml", old)
		}
	}
	return nil
}

func decodeMap(raw []byte) (map[string]any, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func childMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	child, _ := m[key].(map[string]any)
	if child == nil {
		return map[string]any{}
	}
	return child
}

// FullProvision loads and validates a FullProvision file.
func FullProvision(path string) (*v1.FullProvision, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var fp v1.FullProvision
	if err := yaml.Unmarshal(raw, &fp); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := ValidateFullProvision(&fp); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	return &fp, nil
}

// PackageDefinition loads and validates a package.yaml.
func PackageDefinition(path string) (*v1.PackageDefinition, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := rejectOldPackageFields(raw); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	var pd v1.PackageDefinition
	if err := yaml.Unmarshal(raw, &pd); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := ValidatePackageDefinition(&pd); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	return &pd, nil
}

// PackageDescriptor loads and validates an install/resource descriptor.
func PackageDescriptor(path string) (*v1.PackageDescriptor, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := rejectOldDescriptorFields(raw); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	var d v1.PackageDescriptor
	if err := yaml.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := ValidatePackageDescriptor(&d); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	return &d, nil
}

func ValidateProvision(p *v1.Provision) error {
	if p.APIVersion != v1.APIVersion {
		return fmt.Errorf("unsupported apiVersion %q; want %q", p.APIVersion, v1.APIVersion)
	}
	if p.Kind != v1.KindProvision {
		return fmt.Errorf("kind %q; want %q", p.Kind, v1.KindProvision)
	}
	if p.Metadata.Name == "" {
		return fmt.Errorf("metadata.name is required")
	}
	// Empty sources/repositories are allowed at the schema level so that the
	// `init` scaffold round-trips through `check`. `expand` enforces
	// non-empty before rendering; see IsScaffold.
	names := map[string]bool{}
	for i, s := range p.Spec.Sources {
		if s.Name == "" {
			return fmt.Errorf("spec.sources[%d].name is required", i)
		}
		if names[s.Name] {
			return fmt.Errorf("duplicate source name %q", s.Name)
		}
		names[s.Name] = true
		if s.Type != "filesystem" {
			return fmt.Errorf("spec.sources[%d].type %q unsupported (v0.1: filesystem only)", i, s.Type)
		}
		if s.Path == "" {
			return fmt.Errorf("spec.sources[%d].path is required", i)
		}
	}
	repoNames := map[string]bool{}
	// Repository names may carry the literal "{{.Env}}" token at this
	// stage; we record both the raw and (when present) the prefix-only
	// form so controller references can be checked against the on-disk
	// name the user typed.
	for i, r := range p.Spec.Repositories {
		if r.Name == "" {
			return fmt.Errorf("spec.repositories[%d].name is required", i)
		}
		if repoNames[r.Name] {
			return fmt.Errorf("duplicate repository name %q", r.Name)
		}
		repoNames[r.Name] = true
		switch r.Type {
		case "k8s-gitops-generic":
			if r.RepoRef != nil {
				return fmt.Errorf("spec.repositories[%d] (%s): repoRef is only valid on k8s-gitops-env repositories", i, r.Name)
			}
			if len(r.Packages) == 0 {
				return fmt.Errorf("spec.repositories[%d] (%s): packages is required for k8s-gitops-generic", i, r.Name)
			}
			if err := validateRepositoryPackages(fmt.Sprintf("spec.repositories[%d].packages", i), r.Packages, false); err != nil {
				return err
			}
		case "k8s-gitops-env":
			if r.RepoRef == nil || r.RepoRef.Name == "" {
				return fmt.Errorf("spec.repositories[%d] (%s): repoRef.name is required for k8s-gitops-env", i, r.Name)
			}
			if err := validateRepositoryPackages(fmt.Sprintf("spec.repositories[%d].packages", i), r.Packages, true); err != nil {
				return err
			}
		default:
			return fmt.Errorf("spec.repositories[%d].type %q invalid (k8s-gitops-generic | k8s-gitops-env)", i, r.Type)
		}
	}
	if err := validateControllers(p.Spec.Controllers, repoNames); err != nil {
		return err
	}
	return nil
}

// validateControllers checks the schema-level shape of the Controllers
// block: every referenced repo must exist in spec.repositories. Role /
// domain consistency is enforced later by expand once the catalog is
// loaded.
func validateControllers(c *v1.Controllers, repoNames map[string]bool) error {
	if c == nil {
		return nil
	}
	check := func(field string, a *v1.ControllerAssignment) error {
		if a == nil {
			return nil
		}
		if a.Repo == "" {
			return fmt.Errorf("spec.controllers.%s.repo is required", field)
		}
		if a.Instance == "" {
			return fmt.Errorf("spec.controllers.%s.instance is required", field)
		}
		if !repoNames[a.Repo] {
			return fmt.Errorf("spec.controllers.%s.repo %q is not declared in spec.repositories", field, a.Repo)
		}
		return nil
	}
	if err := check("kubernetesResources", c.KubernetesResources); err != nil {
		return err
	}
	if err := check("serviceResources", c.ServiceResources); err != nil {
		return err
	}
	return nil
}

func validateRepositoryPackages(prefix string, packages []v1.PackageRef, envRepo bool) error {
	for i, pr := range packages {
		pp := fmt.Sprintf("%s[%d]", prefix, i)
		if pr.Template == "" {
			return fmt.Errorf("%s.template is required", pp)
		}
		if pr.InstallMethod != "" && !validRenderer(pr.InstallMethod) {
			return fmt.Errorf("%s.installMethod %q invalid (olm | kustomize | helm | raw)", pp, pr.InstallMethod)
		}
		if envRepo && pr.InstallMethod != "" {
			return fmt.Errorf("%s.installMethod is only valid on k8s-gitops-generic repositories", pp)
		}
		if !envRepo && len(pr.Resources) > 0 {
			return fmt.Errorf("%s.resources is only valid on k8s-gitops-env repositories", pp)
		}
		for j, r := range pr.Resources {
			rp := fmt.Sprintf("%s.resources[%d]", pp, j)
			if r.Template == "" {
				return fmt.Errorf("%s.template is required", rp)
			}
			if r.Name == "" {
				return fmt.Errorf("%s.name is required", rp)
			}
		}
	}
	return nil
}

func validRenderer(r string) bool {
	switch r {
	case v1.RendererOLM, v1.RendererKustomize, v1.RendererHelm, v1.RendererRaw:
		return true
	}
	return false
}

// IsScaffold reports whether a Provision still carries the init-time empty
// sources/repositories. Used by `expand` to refuse rendering and by `check`
// to distinguish scaffold state from a malformed file.
func IsScaffold(p *v1.Provision) bool {
	return len(p.Spec.Sources) == 0 && len(p.Spec.Repositories) == 0
}

func ValidateFullProvision(fp *v1.FullProvision) error {
	if fp.APIVersion != v1.APIVersion {
		return fmt.Errorf("unsupported apiVersion %q; want %q", fp.APIVersion, v1.APIVersion)
	}
	if fp.Kind != v1.KindFullProvision {
		return fmt.Errorf("kind %q; want %q", fp.Kind, v1.KindFullProvision)
	}
	if fp.Metadata.Name == "" {
		return fmt.Errorf("metadata.name is required")
	}
	if fp.Spec.Repository.Layout == "" {
		return fmt.Errorf("spec.repository.layout is required")
	}
	if fp.Spec.Repository.Layout != "split" {
		return fmt.Errorf("spec.repository.layout %q unsupported (v0.1: split only)", fp.Spec.Repository.Layout)
	}
	if len(fp.Spec.Packages) == 0 {
		return fmt.Errorf("spec.packages must contain at least one package")
	}
	seen := map[string]bool{}
	for i, rp := range fp.Spec.Packages {
		prefix := fmt.Sprintf("spec.packages[%d]", i)
		if rp.Instance == "" {
			return fmt.Errorf("%s.instance is required", prefix)
		}
		if seen[rp.Instance] {
			return fmt.Errorf("duplicate instance %q", rp.Instance)
		}
		seen[rp.Instance] = true
		switch rp.UnitType {
		case v1.UnitTypeInstall:
			if rp.InstallMethod == "" {
				return fmt.Errorf("%s.installMethod is required for unitType=install", prefix)
			}
		case v1.UnitTypeResource:
			if rp.ResourceTemplate == "" {
				return fmt.Errorf("%s.resourceTemplate is required for unitType=resource", prefix)
			}
			if rp.ResourceName == "" {
				return fmt.Errorf("%s.resourceName is required for unitType=resource", prefix)
			}
		default:
			return fmt.Errorf("%s.unitType %q invalid (install | resource)", prefix, rp.UnitType)
		}
		if rp.Repository == "" {
			return fmt.Errorf("%s.repository is required", prefix)
		}
		if rp.Renderer == "" {
			return fmt.Errorf("%s.renderer is required", prefix)
		}
		if rp.RenderedPaths.Repo == "" || rp.RenderedPaths.Dir == "" {
			return fmt.Errorf("%s.renderedPaths.repo and renderedPaths.dir are required", prefix)
		}
	}
	return nil
}

func ValidatePackageDefinition(pd *v1.PackageDefinition) error {
	if pd.APIVersion != v1.APIVersion {
		return fmt.Errorf("unsupported apiVersion %q; want %q", pd.APIVersion, v1.APIVersion)
	}
	if pd.Kind != v1.KindPackageDefinition {
		return fmt.Errorf("kind %q; want %q", pd.Kind, v1.KindPackageDefinition)
	}
	if pd.Metadata.Name == "" {
		return fmt.Errorf("metadata.name is required")
	}
	switch pd.Spec.Role {
	case v1.RoleKRC, v1.RoleSRC, v1.RoleWorkload:
	default:
		return fmt.Errorf("spec.role %q invalid (%s | %s | %s)",
			pd.Spec.Role, v1.RoleKRC, v1.RoleSRC, v1.RoleWorkload)
	}
	if pd.Spec.DefaultInstall == "" {
		return fmt.Errorf("spec.defaultInstall is required")
	}
	if !validRenderer(pd.Spec.DefaultInstall) {
		return fmt.Errorf("spec.defaultInstall %q invalid (olm | kustomize | helm | raw)", pd.Spec.DefaultInstall)
	}
	if err := validateControllerCLI(pd); err != nil {
		return err
	}
	if err := validateControllerReadiness(pd); err != nil {
		return err
	}
	seenResource := map[string]bool{}
	for i, r := range pd.Spec.DefaultResources {
		if r.Template == "" {
			return fmt.Errorf("spec.defaultResources[%d].template is required", i)
		}
		if r.Name == "" {
			return fmt.Errorf("spec.defaultResources[%d].name is required", i)
		}
		key := r.Template + "/" + r.Name
		if seenResource[key] {
			return fmt.Errorf("duplicate default resource %q", key)
		}
		seenResource[key] = true
	}
	return nil
}

// validateControllerCLI enforces CLI shape rules per role: SRC must
// declare a binary, workloads must not declare cli, KRC may optionally
// declare one. Args are validated lazily at apply-render time.
func validateControllerCLI(pd *v1.PackageDefinition) error {
	cli := pd.Spec.CLI
	switch pd.Spec.Role {
	case v1.RoleSRC:
		if cli == nil || cli.Binary == "" {
			return fmt.Errorf("spec.cli.binary is required for role %q", pd.Spec.Role)
		}
	case v1.RoleWorkload:
		if cli != nil {
			return fmt.Errorf("spec.cli is only valid for controller packages (role %q or %q)",
				v1.RoleKRC, v1.RoleSRC)
		}
	}
	return nil
}

// validateControllerReadiness requires controller packages to declare at
// least one readiness check so gitups apply knows when the operator is
// live and handoff is safe.
func validateControllerReadiness(pd *v1.PackageDefinition) error {
	if pd.Spec.Role == v1.RoleKRC || pd.Spec.Role == v1.RoleSRC {
		if len(pd.Spec.Readiness) == 0 {
			return fmt.Errorf("spec.readiness[] is required for role %q", pd.Spec.Role)
		}
		for i, c := range pd.Spec.Readiness {
			if c.Kind == "" || c.Name == "" || c.Condition == "" {
				return fmt.Errorf("spec.readiness[%d]: kind, name, and condition are all required", i)
			}
		}
	}
	return nil
}

func ValidatePackageDescriptor(d *v1.PackageDescriptor) error {
	if d.APIVersion != v1.APIVersion {
		return fmt.Errorf("unsupported apiVersion %q; want %q", d.APIVersion, v1.APIVersion)
	}
	if d.Kind != v1.KindPackageDescriptor {
		return fmt.Errorf("kind %q; want %q", d.Kind, v1.KindPackageDescriptor)
	}
	inputNames := map[string]bool{}
	for i, in := range d.Spec.Inputs {
		if in.Name == "" {
			return fmt.Errorf("spec.inputs[%d].name is required", i)
		}
		if in.Type == "" {
			return fmt.Errorf("spec.inputs[%d].type is required", i)
		}
		if inputNames[in.Name] {
			return fmt.Errorf("duplicate input %q", in.Name)
		}
		inputNames[in.Name] = true
	}
	return validateRenderable("spec", d.Spec.Renderer, d.Spec.OLM, d.Spec.Helm, d.Spec.Kustomize)
}

func validateRenderable(prefix, renderer string, olm *v1.OLMSpec, helm *v1.HelmSpec, kustomize *v1.KustomizeSpec) error {
	switch renderer {
	case v1.RendererOLM:
		if olm == nil {
			return fmt.Errorf("%s.olm is required when renderer=olm", prefix)
		}
		if olm.Package == "" || olm.Channel == "" || olm.Source == "" || olm.SourceNamespace == "" || olm.StartingCSV == "" {
			return fmt.Errorf("%s.olm: package, channel, source, sourceNamespace, and startingCSV are all required", prefix)
		}
		if olm.InstallPlanApproval != "" && olm.InstallPlanApproval != "Automatic" && olm.InstallPlanApproval != "Manual" {
			return fmt.Errorf("%s.olm.installPlanApproval %q invalid (Automatic | Manual)", prefix, olm.InstallPlanApproval)
		}
		switch olm.OperatorGroupScope {
		case "", "AllNamespaces", "OwnNamespace", "SingleNamespace":
		default:
			return fmt.Errorf("%s.olm.operatorGroupScope %q invalid (AllNamespaces | OwnNamespace | SingleNamespace)", prefix, olm.OperatorGroupScope)
		}
	case v1.RendererHelm:
		if helm == nil {
			return fmt.Errorf("%s.helm is required when renderer=helm", prefix)
		}
		if helm.Chart == "" || helm.Version == "" || helm.Repo == "" {
			return fmt.Errorf("%s.helm: repo, chart, and version are all required", prefix)
		}
	case v1.RendererKustomize:
		if kustomize == nil {
			return fmt.Errorf("%s.kustomize is required when renderer=kustomize", prefix)
		}
		if kustomize.Base == "" {
			return fmt.Errorf("%s.kustomize.base is required", prefix)
		}
	case v1.RendererRaw:
		// no extra block required
	default:
		return fmt.Errorf("%s.renderer %q invalid (olm | kustomize | helm | raw)", prefix, renderer)
	}
	return nil
}
