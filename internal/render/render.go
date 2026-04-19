// Package render implements stage 2 of gitups: consume a FullProvision and
// write a split-layout output tree (one directory per logical repo).
//
// The renderer never reads Provision directly; it operates on the resolved
// FullProvision produced by package resolve.
package render

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
	"github.com/crmarques/gitups/internal/catalog"
	"github.com/crmarques/gitups/internal/placeholders"
)

// managedScriptTemplateValues builds the template context the SRC's
// managed-script overlay consumes. Inputs come from the original
// authoring descriptor (scripts/ dir on disk, pinned scriptImage / SA)
// and from the unit's ResolvedValues (namespace + the full original
// value map serialised verbatim as valuesJSON so the script body reads
// exactly what it did under the retired built-in wrapper). Called at
// render time only; ResolvedValues is never mutated so placeholder
// tracking stays per-field on the source unit.
func managedScriptTemplateValues(rp *v1.ResolvedPackage, origUnit catalog.Unit) (map[string]any, error) {
	namespace, _ := rp.ResolvedValues["namespace"].(string)
	if namespace == "" {
		return nil, fmt.Errorf("resolvedValues.namespace is required for managed-script resources")
	}
	scripts, err := readManagedScriptBodies(filepath.Join(origUnit.SourceDir, "scripts"))
	if err != nil {
		return nil, err
	}
	if _, ok := scripts["provision.sh"]; !ok {
		return nil, fmt.Errorf("scripts/provision.sh missing in %s", origUnit.SourceDir)
	}
	valuesJSON, err := json.MarshalIndent(rp.ResolvedValues, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal resolved values: %w", err)
	}
	sa := origUnit.Descriptor.ScriptServiceAccount
	if sa == "" {
		sa = "default"
	}
	return map[string]any{
		"namespace":            namespace,
		"scriptImage":          origUnit.Descriptor.ScriptImage,
		"scriptServiceAccount": sa,
		"scripts":              scripts,
		"valuesJSON":           string(valuesJSON),
	}, nil
}

// readManagedScriptBodies returns a deterministic filename→body map for
// every *.sh file in dir. Subdirectories and non-shell files are
// ignored.
func readManagedScriptBodies(dir string) (map[string]any, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sh") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	out := map[string]any{}
	for _, n := range names {
		body, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", n, err)
		}
		out[n] = string(body)
	}
	return out, nil
}

// Options controls Render behavior.
type Options struct {
	OutputPath        string
	KubectlContext    string
	AllowPlaceholders bool
	Helm              HelmRunner
	Kustomize         KustomizeRunner
	Hooks             HookRunner
	SourceFullProv    string // optional path to the input FullProvision for traceability copy
	// SuppressFullProvision disables writing full-provision.yaml at the top of
	// OutputPath. Set by the workspace-layout CLI where the authoritative
	// FullProvision lives one level up and re-writing it here would clobber
	// the user's formatting/comments.
	SuppressFullProvision bool
	// PreserveExtras, when true, keeps existing top-level entries in OutputPath
	// that the renderer did not produce (e.g. sibling provision.yaml /
	// full-provision.yaml managed by the CLI workspace). Only entries matching
	// a rendered repo dir or trace file are replaced. Defaults to false, which
	// preserves legacy nuke-and-rename behavior for callers that own the whole
	// tree.
	PreserveExtras bool
	// Prune, used together with PreserveExtras, removes top-level directories
	// in OutputPath that are not one of the repos emitted by this render pass.
	// Workspace-managed files (provision.yaml / full-provision.yaml) are
	// always preserved. No effect when PreserveExtras is false.
	Prune bool
}

// Render writes the full output tree for fp under opts.OutputPath. It renders
// into a temporary sibling directory first and swaps it into place atomically.
func Render(ctx context.Context, fp *v1.FullProvision, cat *catalog.Catalog, opts Options) error {
	if !opts.AllowPlaceholders {
		if err := failOnPlaceholders(fp); err != nil {
			return err
		}
	}
	if opts.Helm == nil {
		opts.Helm = NewExecHelmRunner("helm")
	}
	if opts.Kustomize == nil {
		opts.Kustomize = NewExecKustomizeRunner("kustomize")
	}
	if opts.Hooks == nil {
		opts.Hooks = NewExecHookRunner()
	}
	if opts.OutputPath == "" {
		opts.OutputPath = fp.Spec.Repository.OutputPath
	}
	if opts.OutputPath == "" {
		return fmt.Errorf("outputPath is empty; set --out or spec.repository.outputPath")
	}

	// Collect per-repo package sets so we can write top-level kustomization
	// and READMEs after per-package rendering.
	byRepo := map[string][]*v1.ResolvedPackage{}

	tempDir, err := os.MkdirTemp(filepath.Dir(absOrCwd(opts.OutputPath)), ".gitups-render-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(tempDir)
		}
	}()

	for i := range fp.Spec.Packages {
		rp := &fp.Spec.Packages[i]
		entry, ok := cat.Lookup(rp.Template)
		if !ok {
			return fmt.Errorf("package %s: template %q not in catalog", rp.Instance, rp.Template)
		}
		pkgDir := filepath.Join(tempDir, rp.RenderedPaths.Repo, rp.RenderedPaths.Dir)
		if err := os.MkdirAll(pkgDir, 0o755); err != nil {
			return fmt.Errorf("package %s: mkdir %s: %w", rp.Instance, pkgDir, err)
		}
		if err := renderPackage(ctx, rp, entry, cat, pkgDir, fp, opts); err != nil {
			return fmt.Errorf("package %s: %w", rp.Instance, err)
		}
		byRepo[rp.RenderedPaths.Repo] = append(byRepo[rp.RenderedPaths.Repo], rp)
	}

	if err := writeRepoToplevel(tempDir, byRepo, fp); err != nil {
		return err
	}

	if !opts.SuppressFullProvision {
		if opts.SourceFullProv != "" {
			src, err := os.ReadFile(opts.SourceFullProv)
			if err != nil {
				return fmt.Errorf("copy full-provision: %w", err)
			}
			if err := os.WriteFile(filepath.Join(tempDir, "full-provision.yaml"), src, 0o644); err != nil {
				return fmt.Errorf("write full-provision copy: %w", err)
			}
		} else {
			out, err := yaml.Marshal(fp)
			if err != nil {
				return fmt.Errorf("marshal full-provision: %w", err)
			}
			if err := os.WriteFile(filepath.Join(tempDir, "full-provision.yaml"), out, 0o644); err != nil {
				return fmt.Errorf("write full-provision: %w", err)
			}
		}
	}

	absOut, err := filepath.Abs(opts.OutputPath)
	if err != nil {
		return fmt.Errorf("resolve outputPath: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absOut), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(absOut), err)
	}

	if opts.PreserveExtras {
		if err := swapEntries(tempDir, absOut); err != nil {
			return err
		}
		if opts.Prune {
			keep := map[string]bool{}
			for repo := range byRepo {
				keep[repo] = true
			}
			if err := pruneOrphanDirs(absOut, keep); err != nil {
				return err
			}
		}
	} else {
		if _, err := os.Stat(absOut); err == nil {
			if err := os.RemoveAll(absOut); err != nil {
				return fmt.Errorf("remove existing outputPath: %w", err)
			}
		}
		if err := os.Rename(tempDir, absOut); err != nil {
			return fmt.Errorf("rename %s -> %s: %w", tempDir, absOut, err)
		}
	}
	committed = true
	return nil
}

// swapEntries moves every top-level entry from src into dst, replacing any
// existing same-named entry in dst. Entries in dst that have no counterpart in
// src are left alone. Not fully atomic across entries (rename is atomic within
// a single entry); the trade-off is that siblings the caller maintains in dst
// are preserved.
func swapEntries(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dst, err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("read temp %s: %w", src, err)
	}
	for _, e := range entries {
		dstEntry := filepath.Join(dst, e.Name())
		if _, err := os.Stat(dstEntry); err == nil {
			if err := os.RemoveAll(dstEntry); err != nil {
				return fmt.Errorf("remove %s: %w", dstEntry, err)
			}
		}
		if err := os.Rename(filepath.Join(src, e.Name()), dstEntry); err != nil {
			return fmt.Errorf("rename %s: %w", e.Name(), err)
		}
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("remove temp %s: %w", src, err)
	}
	return nil
}

// pruneOrphanDirs removes top-level directories under dst whose names are not
// in keep. Files (provision.yaml / full-provision.yaml) are always left alone.
func pruneOrphanDirs(dst string, keep map[string]bool) error {
	entries, err := os.ReadDir(dst)
	if err != nil {
		return fmt.Errorf("read %s for prune: %w", dst, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if keep[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dst, e.Name())); err != nil {
			return fmt.Errorf("prune %s: %w", e.Name(), err)
		}
	}
	return nil
}

func absOrCwd(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "."
	}
	return abs
}

func failOnPlaceholders(fp *v1.FullProvision) error {
	var paths []string
	for i := range fp.Spec.Packages {
		rp := &fp.Spec.Packages[i]
		if placeholders.Contains(rp.ResolvedValues) {
			paths = append(paths, rp.Instance)
		}
	}
	if len(paths) > 0 {
		sort.Strings(paths)
		return fmt.Errorf("unfilled placeholders in: %s (re-run with --allow-placeholders to force)", strings.Join(paths, ", "))
	}
	return nil
}

type renderUnit struct {
	SourceDir string
	Renderer  string
	OLM       *v1.OLMSpec
	Helm      *v1.HelmSpec
	Kustomize *v1.KustomizeSpec
	Hooks     *v1.HookSpec
	Overlays  []string
	Readiness []v1.ReadinessCheck
}

// controllerDomainFor maps a controller Role to its owning package
// directory domain. Every controller kind implements its intents under
// exactly one domain so the mapping is a small lookup.
func controllerDomainFor(kind v1.Role) string {
	switch kind {
	case v1.RoleKRC:
		return v1.DomainKRC
	case v1.RoleSRC:
		return v1.DomainSRC
	}
	return ""
}

func renderUnitFor(rp *v1.ResolvedPackage, entry catalog.Entry, cat *catalog.Catalog) (renderUnit, error) {
	// Controller-routed units: the unit's render shape (templates +
	// renderer) is owned by the controller package's intent
	// implementation, but the readiness checks stay on the original
	// authoring descriptor — they describe the workload object the
	// cluster must wait for, not the controller itself (the controller's
	// own readiness is checked during apply-phase handoff).
	if rp.Controller != nil && rp.Controller.Intent != "" {
		ctrlEntry, ok := cat.Lookup(rp.Controller.Template)
		if !ok {
			return renderUnit{}, fmt.Errorf("controller package %q not in catalog", rp.Controller.Template)
		}
		domain := controllerDomainFor(rp.Controller.Kind)
		u, ok := ctrlEntry.LookupDomainUnit(domain, rp.Controller.Intent)
		if !ok {
			return renderUnit{}, fmt.Errorf("%s package %q does not implement intent %q",
				domain, ctrlEntry.Def.Metadata.Name, rp.Controller.Intent)
		}
		// managed-resource is interface passthrough: the CR body comes
		// from the PROVIDER's resources/<resourceTemplate>/ directory,
		// not from the SRC's intent directory. The SRC's intent dir
		// just declares "I can reconcile CRs for this intent"; the CR
		// shape lives with the service that owns the interface.
		if rp.Controller.Intent == "managed-resource" {
			orig, ok := entry.LookupDomainUnit(v1.DomainResources, rp.ResourceTemplate)
			if !ok {
				return renderUnit{}, fmt.Errorf("managed-resource: provider package %q has no resources/%s/ template",
					entry.Def.Metadata.Name, rp.ResourceTemplate)
			}
			return renderUnit{
				SourceDir: orig.SourceDir,
				Renderer:  orig.Descriptor.Renderer,
				OLM:       orig.Descriptor.OLM,
				Helm:      orig.Descriptor.Helm,
				Kustomize: orig.Descriptor.Kustomize,
				Hooks:     orig.Descriptor.Hooks,
				Overlays:  append([]string(nil), orig.Descriptor.Overlays...),
				Readiness: append([]v1.ReadinessCheck(nil), orig.Descriptor.Readiness...),
			}, nil
		}
		readiness := append([]v1.ReadinessCheck(nil), u.Descriptor.Readiness...)
		if orig, ok := entry.LookupDomainUnit(rp.Domain, rp.ResourceTemplate); ok && len(orig.Descriptor.Readiness) > 0 {
			readiness = append([]v1.ReadinessCheck(nil), orig.Descriptor.Readiness...)
		}
		return renderUnit{
			SourceDir: u.SourceDir,
			Renderer:  u.Descriptor.Renderer,
			OLM:       u.Descriptor.OLM,
			Helm:      u.Descriptor.Helm,
			Kustomize: u.Descriptor.Kustomize,
			Hooks:     u.Descriptor.Hooks,
			Overlays:  append([]string(nil), u.Descriptor.Overlays...),
			Readiness: readiness,
		}, nil
	}
	switch rp.UnitType {
	case v1.UnitTypeInstall:
		u, ok := entry.LookupDomainUnit(v1.DomainInstall, rp.InstallMethod)
		if !ok {
			return renderUnit{}, fmt.Errorf("install method %q not declared by package %q", rp.InstallMethod, entry.Def.Metadata.Name)
		}
		return renderUnit{
			SourceDir: u.SourceDir,
			Renderer:  u.Descriptor.Renderer,
			OLM:       u.Descriptor.OLM,
			Helm:      u.Descriptor.Helm,
			Kustomize: u.Descriptor.Kustomize,
			Hooks:     u.Descriptor.Hooks,
			Overlays:  append([]string(nil), u.Descriptor.Overlays...),
			Readiness: append([]v1.ReadinessCheck(nil), u.Descriptor.Readiness...),
		}, nil
	case v1.UnitTypeResource:
		domain := rp.Domain
		if domain == "" {
			domain = v1.DomainResources
		}
		u, ok := entry.LookupDomainUnit(domain, rp.ResourceTemplate)
		if !ok {
			return renderUnit{}, fmt.Errorf("domain %q sub-unit %q not declared by package %q", domain, rp.ResourceTemplate, entry.Def.Metadata.Name)
		}
		return renderUnit{
			SourceDir: u.SourceDir,
			Renderer:  u.Descriptor.Renderer,
			OLM:       u.Descriptor.OLM,
			Helm:      u.Descriptor.Helm,
			Kustomize: u.Descriptor.Kustomize,
			Hooks:     u.Descriptor.Hooks,
			Overlays:  append([]string(nil), u.Descriptor.Overlays...),
			Readiness: append([]v1.ReadinessCheck(nil), u.Descriptor.Readiness...),
		}, nil
	default:
		return renderUnit{}, fmt.Errorf("unknown unitType %q", rp.UnitType)
	}
}

// renderPackage dispatches on renderer type, runs hooks, and writes overlays.
func renderPackage(ctx context.Context, rp *v1.ResolvedPackage, entry catalog.Entry, cat *catalog.Catalog, pkgDir string, fp *v1.FullProvision, opts Options) error {
	unit, err := renderUnitFor(rp, entry, cat)
	if err != nil {
		return err
	}
	templateValues := rp.ResolvedValues
	if rp.Controller != nil && rp.Controller.Intent == "managed-script" {
		origUnit, ok := entry.LookupDomainUnit(rp.Domain, rp.ResourceTemplate)
		if !ok {
			return fmt.Errorf("managed-script rewire: original unit %q not found in package %q", rp.ResourceTemplate, entry.Def.Metadata.Name)
		}
		v, err := managedScriptTemplateValues(rp, origUnit)
		if err != nil {
			return err
		}
		templateValues = v
	}
	tctx := templateCtx{
		Values:           templateValues,
		Instance:         rp.Instance,
		PackageInstance:  rp.PackageInstance,
		UnitType:         rp.UnitType,
		ResourceTemplate: rp.ResourceTemplate,
		ResourceName:     rp.ResourceName,
		Env:              fp.Metadata.Name,
		Context:          opts.KubectlContext,
	}

	if err := runHook(ctx, unit, "pre-render", pkgDir, rp.ResolvedValues, opts.Hooks); err != nil {
		return err
	}

	switch unit.Renderer {
	case "olm":
		if err := renderOLM(ctx, rp, unit, pkgDir, tctx); err != nil {
			return err
		}
	case "helm":
		if err := renderHelm(ctx, rp, unit, pkgDir, tctx, opts.Helm); err != nil {
			return err
		}
	case "kustomize":
		if err := renderKustomize(ctx, rp, unit, pkgDir, tctx, opts.Kustomize); err != nil {
			return err
		}
	case "raw":
		if err := renderRaw(ctx, rp, unit, pkgDir, tctx); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown renderer %q", unit.Renderer)
	}

	if err := renderOverlays(unit, pkgDir, tctx); err != nil {
		return err
	}

	if err := writePackageKustomization(pkgDir, rp, unit, tctx); err != nil {
		return err
	}

	return runHook(ctx, unit, "post-render", pkgDir, rp.ResolvedValues, opts.Hooks)
}

func writeRepoToplevel(tempDir string, byRepo map[string][]*v1.ResolvedPackage, fp *v1.FullProvision) error {
	repos := make([]string, 0, len(byRepo))
	for r := range byRepo {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	for _, repo := range repos {
		pkgs := byRepo[repo]
		sort.SliceStable(pkgs, func(i, j int) bool { return pkgs[i].Instance < pkgs[j].Instance })
		repoDir := filepath.Join(tempDir, repo)

		resourceSet := map[string]bool{}
		parentResources := map[string]map[string]bool{}
		for _, rp := range pkgs {
			dir := filepath.ToSlash(rp.RenderedPaths.Dir)
			parts := strings.Split(dir, "/")
			if len(parts) >= 3 && parts[0] == "packages" {
				parent := filepath.Join(parts[0], parts[1])
				resourceSet[parent] = true
				for i := 2; i < len(parts); i++ {
					p := filepath.Join(parts[:i]...)
					if parentResources[p] == nil {
						parentResources[p] = map[string]bool{}
					}
					parentResources[p][parts[i]] = true
				}
				continue
			}
			resourceSet[rp.RenderedPaths.Dir] = true
		}
		for parent, children := range parentResources {
			parentDir := filepath.Join(repoDir, parent)
			if err := os.MkdirAll(parentDir, 0o755); err != nil {
				return fmt.Errorf("repo %s: mkdir %s: %w", repo, parent, err)
			}
			if err := writeKustomization(filepath.Join(parentDir, "kustomization.yaml"), sortedKeys(children)); err != nil {
				return fmt.Errorf("repo %s: write %s/kustomization.yaml: %w", repo, parent, err)
			}
		}
		if err := writeKustomization(filepath.Join(repoDir, "kustomization.yaml"), sortedKeys(resourceSet)); err != nil {
			return fmt.Errorf("repo %s: write kustomization: %w", repo, err)
		}

		// README
		var b strings.Builder
		fmt.Fprintf(&b, "# %s\n\nRepo rendered by gitups from FullProvision %q.\n\n",
			repo, fp.Metadata.Name)
		fmt.Fprintf(&b, "## Packages\n\n")
		for _, rp := range pkgs {
			fmt.Fprintf(&b, "- **%s** (%s, %s, renderer=%s, role=%s) -> `%s`\n",
				rp.Instance, rp.Template, rp.UnitType, rp.Renderer, rp.Role, rp.RenderedPaths.Dir)
		}
		if len(fp.Spec.Placeholders) > 0 {
			fmt.Fprintf(&b, "\n## Unfilled placeholders at render time\n\n")
			for _, ph := range fp.Spec.Placeholders {
				fmt.Fprintf(&b, "- `%s` — %s\n", ph.Path, ph.Reason)
			}
		}
		if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte(b.String()), 0o644); err != nil {
			return fmt.Errorf("repo %s: write README: %w", repo, err)
		}
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func writeKustomization(path string, resources []string) error {
	kz := map[string]any{
		"apiVersion": "kustomize.config.k8s.io/v1beta1",
		"kind":       "Kustomization",
		"resources":  resources,
	}
	body, err := yaml.Marshal(kz)
	if err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}

// writePackageKustomization writes a kustomization.yaml listing every .yaml
// file in pkgDir except values.yaml (which is human-readable helm inputs, not
// a k8s manifest) and kustomization.yaml itself. It also emits
// commonAnnotations carrying the resolved apply-wave and readiness checks so
// every manifest in the package is labeled without needing to re-parse YAML.
func writePackageKustomization(pkgDir string, rp *v1.ResolvedPackage, unit renderUnit, tctx templateCtx) error {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return err
	}
	var resources []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}
		if name == "kustomization.yaml" || name == "values.yaml" {
			continue
		}
		resources = append(resources, name)
	}
	sort.Strings(resources)

	annotations := map[string]string{
		v1.ApplyWaveAnnotation: fmt.Sprintf("%d", rp.ApplyWave),
	}
	if len(unit.Readiness) > 0 {
		expanded, err := expandReadiness(unit.Readiness, tctx)
		if err != nil {
			return err
		}
		encoded, err := encodeReadiness(expanded)
		if err != nil {
			return err
		}
		annotations[v1.ReadinessAnnotation] = encoded
	}

	kz := map[string]any{
		"apiVersion":        "kustomize.config.k8s.io/v1beta1",
		"kind":              "Kustomization",
		"commonAnnotations": annotations,
		"resources":         resources,
	}
	body, err := yaml.Marshal(kz)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(pkgDir, "kustomization.yaml"), body, 0o644)
}

// expandReadiness walks each readiness entry and renders its template-bearing
// fields (Name, Namespace) through the same template context the renderer
// uses for overlays. Kind and Condition are treated as literals.
func expandReadiness(checks []v1.ReadinessCheck, tctx templateCtx) ([]v1.ReadinessCheck, error) {
	out := make([]v1.ReadinessCheck, len(checks))
	for i, c := range checks {
		out[i] = c
		name, err := renderTemplateString("readiness.name", c.Name, tctx)
		if err != nil {
			return nil, fmt.Errorf("readiness[%d].name: %w", i, err)
		}
		out[i].Name = name
		if c.Namespace != "" {
			ns, err := renderTemplateString("readiness.namespace", c.Namespace, tctx)
			if err != nil {
				return nil, fmt.Errorf("readiness[%d].namespace: %w", i, err)
			}
			out[i].Namespace = ns
		}
	}
	return out, nil
}

// encodeReadiness serializes readiness checks as compact JSON, suitable for
// an annotation value.
func encodeReadiness(checks []v1.ReadinessCheck) (string, error) {
	b, err := json.Marshal(checks)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
