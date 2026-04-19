// Package catalog resolves package sources to PackageDefinitions. v0.1 only
// supports the filesystem source driver.
package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
	"github.com/crmarques/gitups/internal/load"
)

// validateProvisioningStyle enforces the "crd" | "script" contract on
// resource descriptors. Install descriptors may not declare a non-default
// provisioningStyle — install units always use their own renderer. When
// style is "script", the descriptor must ship scripts/provision.sh.
func validateProvisioningStyle(descPath string, install bool, unitDir string, spec v1.PackageDescriptorSpec) error {
	style := spec.ProvisioningStyle
	if style == "" || style == v1.ProvisioningStyleCRD {
		return nil
	}
	if install {
		return fmt.Errorf("%s: install descriptors may not set provisioningStyle", descPath)
	}
	if style != v1.ProvisioningStyleScript {
		return fmt.Errorf("%s: provisioningStyle %q: must be %q or %q", descPath, style, v1.ProvisioningStyleCRD, v1.ProvisioningStyleScript)
	}
	if spec.Renderer != v1.RendererRaw {
		return fmt.Errorf("%s: provisioningStyle %q requires renderer %q (got %q)", descPath, style, v1.RendererRaw, spec.Renderer)
	}
	if spec.ScriptImage == "" {
		return fmt.Errorf("%s: provisioningStyle %q requires spec.scriptImage (pinned, non-latest)", descPath, style)
	}
	if strings.Contains(spec.ScriptImage, ":latest") || !strings.ContainsAny(spec.ScriptImage, ":@") {
		return fmt.Errorf("%s: scriptImage %q must be pinned to a tag or digest (no implicit :latest)", descPath, spec.ScriptImage)
	}
	provision := filepath.Join(unitDir, "scripts", "provision.sh")
	if _, err := os.Stat(provision); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s: provisioningStyle %q requires scripts/provision.sh", descPath, style)
		}
		return err
	}
	return nil
}

// validateReadiness verifies static shape on every readiness entry: kind,
// name, and condition are required. Name and namespace may be template
// strings (evaluated at render time) so we only enforce non-emptiness here.
func validateReadiness(descPath string, checks []v1.ReadinessCheck) error {
	for i, c := range checks {
		if c.Kind == "" {
			return fmt.Errorf("%s: readiness[%d].kind is required", descPath, i)
		}
		if c.Name == "" {
			return fmt.Errorf("%s: readiness[%d].name is required", descPath, i)
		}
		if c.Condition == "" {
			return fmt.Errorf("%s: readiness[%d] (%s/%s): condition is required", descPath, i, c.Kind, c.Name)
		}
	}
	return nil
}

// validateExportFrom accepts the small grammar documented on
// v1.CapabilityExport.From: "input:<key>", "binding:<key>", or the literal
// "placeholder". <key> must be non-empty and may be dotted.
func validateExportFrom(from string) error {
	switch {
	case from == "placeholder":
		return nil
	case strings.HasPrefix(from, "input:"):
		if len(from) == len("input:") {
			return fmt.Errorf("from %q: key is required after \"input:\"", from)
		}
	case strings.HasPrefix(from, "binding:"):
		if len(from) == len("binding:") {
			return fmt.Errorf("from %q: key is required after \"binding:\"", from)
		}
	default:
		return fmt.Errorf("from %q: must be \"input:<key>\", \"binding:<key>\", or \"placeholder\"", from)
	}
	return nil
}

// Entry pairs a resolved PackageDefinition with the absolute source directory
// it was loaded from (needed later by the renderer to find overlays, raw
// manifests, and scripts). Units are keyed by domain (install, resources,
// kubernetes-resource-controller, service-resource-controller) and then by
// sub-name (renderer / template / intent).
type Entry struct {
	Source     string // source name (e.g. "local")
	SourceRoot string // absolute path to the source root
	Dir        string // absolute path to the package dir
	Def        *v1.PackageDefinition
	Domains    map[string]map[string]Unit
}

// Installs returns the install-domain units (renderer → Unit) for this
// entry. Nil-safe convenience accessor kept in lockstep with Domains.
func (e Entry) Installs() map[string]Unit { return e.Domains[v1.DomainInstall] }

// Resources returns the resources-domain units (template → Unit).
func (e Entry) Resources() map[string]Unit { return e.Domains[v1.DomainResources] }

// KRCIntents returns the KRC-domain intent implementations (intent → Unit).
func (e Entry) KRCIntents() map[string]Unit { return e.Domains[v1.DomainKRC] }

// SRCIntents returns the SRC-domain intent implementations (intent → Unit).
func (e Entry) SRCIntents() map[string]Unit { return e.Domains[v1.DomainSRC] }

// LookupDomainUnit returns the Unit in the given domain/sub-name pair, or
// false if the package doesn't implement it.
func (e Entry) LookupDomainUnit(domain, subName string) (Unit, bool) {
	units, ok := e.Domains[domain]
	if !ok {
		return Unit{}, false
	}
	u, ok := units[subName]
	return u, ok
}

type Unit struct {
	Name       string
	SourceDir  string
	Descriptor v1.PackageDescriptorSpec
}

// knownDomains is the closed set of top-level package directories gitups
// understands. Any other top-level dir (other than the always-allowed
// bookkeeping entries below) is a load error so typos fail fast.
var knownDomains = map[string]struct{}{
	v1.DomainInstall:   {},
	v1.DomainResources: {},
	v1.DomainKRC:       {},
	v1.DomainSRC:       {},
}

// ignoredDirs are top-level package directories that the catalog loader
// silently skips. They carry no runtime meaning but are valid package-local
// conventions (e.g. the `tests/` smoke-test harness shipped with a package,
// or `.git`/`.github` when a package is later extracted to its own repo).
var ignoredDirs = map[string]struct{}{
	"tests":   {},
	".git":    {},
	".github": {},
}

// isLooseFile is a top-level entry we don't treat as a domain directory —
// package.yaml and README are always present; anything else alongside them
// must be a known domain dir.
func isLooseEntry(name string) bool {
	switch name {
	case "package.yaml", "README.md", "values.schema.json",
		"CHANGELOG.md", "LICENSE", ".gitignore", ".gitattributes":
		return true
	}
	return false
}

// Catalog indexes entries by their qualified name: "<source>/<package>".
type Catalog struct {
	entries map[string]Entry
}

func (c *Catalog) Lookup(qualified string) (Entry, bool) {
	e, ok := c.entries[qualified]
	return e, ok
}

// Qualified returns the sorted list of qualified names present in the catalog.
// Useful for deterministic diagnostics.
func (c *Catalog) Qualified() []string {
	out := make([]string, 0, len(c.entries))
	for k := range c.entries {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Build walks every source referenced by the Provision and returns a populated
// Catalog. Paths in Provision are interpreted relative to baseDir.
func Build(sources []v1.PackageSource, baseDir string) (*Catalog, error) {
	c := &Catalog{entries: map[string]Entry{}}
	for _, s := range sources {
		if s.Type != "filesystem" {
			return nil, fmt.Errorf("source %q: unsupported type %q (v0.1: filesystem)", s.Name, s.Type)
		}
		root := s.Path
		if !filepath.IsAbs(root) {
			root = filepath.Join(baseDir, root)
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("source %q: resolve %s: %w", s.Name, root, err)
		}
		if err := loadFilesystemSource(c, s.Name, abs); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func loadFilesystemSource(c *Catalog, name, root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("source %q: read %s: %w", name, root, err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pkgDir := filepath.Join(root, e.Name())
		pkgYAML := filepath.Join(pkgDir, "package.yaml")
		if _, err := os.Stat(pkgYAML); err != nil {
			continue // directory isn't a package
		}
		def, err := load.PackageDefinition(pkgYAML)
		if err != nil {
			return fmt.Errorf("source %q: %w", name, err)
		}
		if def.Metadata.Name != e.Name() {
			return fmt.Errorf("source %q: package dir %s declares metadata.name %q", name, e.Name(), def.Metadata.Name)
		}
		if seen[def.Metadata.Name] {
			return fmt.Errorf("source %q: duplicate package name %q", name, def.Metadata.Name)
		}
		domains, err := loadDomains(pkgDir)
		if err != nil {
			return fmt.Errorf("source %q: package %s: %w", name, def.Metadata.Name, err)
		}
		if err := validateRoleDomains(name, def, domains); err != nil {
			return err
		}
		installs := domains[v1.DomainInstall]
		resources := domains[v1.DomainResources]
		if _, ok := installs[def.Spec.DefaultInstall]; !ok {
			return fmt.Errorf("source %q: package %s: defaultInstall %q has no install descriptor", name, def.Metadata.Name, def.Spec.DefaultInstall)
		}
		for _, r := range def.Spec.DefaultResources {
			if _, ok := resources[r.Template]; !ok {
				return fmt.Errorf("source %q: package %s: defaultResources references unknown resource template %q", name, def.Metadata.Name, r.Template)
			}
		}
		for i, p := range def.Spec.Provides {
			if p.Capability == "" {
				return fmt.Errorf("source %q: package %s: provides[%d].capability is required", name, def.Metadata.Name, i)
			}
			if p.ResourceTemplate == "" {
				return fmt.Errorf("source %q: package %s: provides[%d] (%s): resourceTemplate is required", name, def.Metadata.Name, i, p.Capability)
			}
			if _, ok := resources[p.ResourceTemplate]; !ok {
				return fmt.Errorf("source %q: package %s: provides[%d] (%s): resourceTemplate %q has no resource descriptor", name, def.Metadata.Name, i, p.Capability, p.ResourceTemplate)
			}
			seenExport := map[string]bool{}
			for j, e := range p.Exports {
				if e.Name == "" {
					return fmt.Errorf("source %q: package %s: provides[%d] (%s) exports[%d].name is required", name, def.Metadata.Name, i, p.Capability, j)
				}
				if seenExport[e.Name] {
					return fmt.Errorf("source %q: package %s: provides[%d] (%s): duplicate export %q", name, def.Metadata.Name, i, p.Capability, e.Name)
				}
				seenExport[e.Name] = true
				if err := validateExportFrom(e.From); err != nil {
					return fmt.Errorf("source %q: package %s: provides[%d] (%s) exports[%d] (%s): %w", name, def.Metadata.Name, i, p.Capability, j, e.Name, err)
				}
			}
		}
		for i, r := range def.Spec.Requires {
			if r.Capability == "" {
				return fmt.Errorf("source %q: package %s: requires[%d].capability is required", name, def.Metadata.Name, i)
			}
			if r.ResourceTemplate == "" {
				return fmt.Errorf("source %q: package %s: requires[%d] (%s): resourceTemplate is required", name, def.Metadata.Name, i, r.Capability)
			}
			if _, ok := resources[r.ResourceTemplate]; !ok {
				return fmt.Errorf("source %q: package %s: requires[%d] (%s): resourceTemplate %q has no resource descriptor", name, def.Metadata.Name, i, r.Capability, r.ResourceTemplate)
			}
			seenInput := map[string]bool{}
			for j, c := range r.Consumes {
				if c.Input == "" {
					return fmt.Errorf("source %q: package %s: requires[%d] (%s) consumes[%d].input is required", name, def.Metadata.Name, i, r.Capability, j)
				}
				if seenInput[c.Input] {
					return fmt.Errorf("source %q: package %s: requires[%d] (%s): duplicate consumes input %q", name, def.Metadata.Name, i, r.Capability, c.Input)
				}
				seenInput[c.Input] = true
				if !strings.HasPrefix(c.From, "provider.") || len(c.From) == len("provider.") {
					return fmt.Errorf("source %q: package %s: requires[%d] (%s) consumes[%d] (%s): from %q must be \"provider.<exportName>\"", name, def.Metadata.Name, i, r.Capability, j, c.Input, c.From)
				}
			}
		}
		seen[def.Metadata.Name] = true
		c.entries[name+"/"+def.Metadata.Name] = Entry{
			Source:     name,
			SourceRoot: root,
			Dir:        pkgDir,
			Def:        def,
			Domains:    domains,
		}
	}
	return nil
}

// loadDomains walks every top-level entry under pkgDir, treats unknown
// entries as an error, and loads each known-domain directory into a
// sub-map keyed by unit sub-name (renderer / template / intent).
func loadDomains(pkgDir string) (map[string]map[string]Unit, error) {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", pkgDir, err)
	}
	out := map[string]map[string]Unit{}
	for _, e := range entries {
		if !e.IsDir() {
			if !isLooseEntry(e.Name()) {
				// Unknown loose files (not package.yaml/README) are allowed;
				// they are harmless package-local notes. Only top-level
				// directories carry domain meaning.
			}
			continue
		}
		name := e.Name()
		if _, ok := ignoredDirs[name]; ok {
			continue
		}
		if _, ok := knownDomains[name]; !ok {
			return nil, fmt.Errorf("%s: unknown domain directory %q (known: %s, %s, %s, %s)",
				pkgDir, name,
				v1.DomainInstall, v1.DomainResources, v1.DomainKRC, v1.DomainSRC)
		}
		units, err := loadUnits(filepath.Join(pkgDir, name), name == v1.DomainInstall)
		if err != nil {
			return nil, err
		}
		out[name] = units
	}
	return out, nil
}

// validateRoleDomains ties the declared role to the domain directories
// present on disk. A role claim must be matched by the corresponding
// domain dir (and no conflicting counterpart).
func validateRoleDomains(source string, def *v1.PackageDefinition, domains map[string]map[string]Unit) error {
	_, hasKRC := domains[v1.DomainKRC]
	_, hasSRC := domains[v1.DomainSRC]
	switch def.Spec.Role {
	case v1.RoleKRC:
		if !hasKRC {
			return fmt.Errorf("source %q: package %s: role %q requires a %q/ directory",
				source, def.Metadata.Name, def.Spec.Role, v1.DomainKRC)
		}
		if hasSRC {
			return fmt.Errorf("source %q: package %s: role %q cannot coexist with %q/ directory",
				source, def.Metadata.Name, def.Spec.Role, v1.DomainSRC)
		}
	case v1.RoleSRC:
		if !hasSRC {
			return fmt.Errorf("source %q: package %s: role %q requires a %q/ directory",
				source, def.Metadata.Name, def.Spec.Role, v1.DomainSRC)
		}
		if hasKRC {
			return fmt.Errorf("source %q: package %s: role %q cannot coexist with %q/ directory",
				source, def.Metadata.Name, def.Spec.Role, v1.DomainKRC)
		}
	case v1.RoleWorkload:
		if hasKRC {
			return fmt.Errorf("source %q: package %s: %q/ directory requires role %q",
				source, def.Metadata.Name, v1.DomainKRC, v1.RoleKRC)
		}
		if hasSRC {
			return fmt.Errorf("source %q: package %s: %q/ directory requires role %q",
				source, def.Metadata.Name, v1.DomainSRC, v1.RoleSRC)
		}
	}
	return nil
}

func loadUnits(root string, install bool) (map[string]Unit, error) {
	out := map[string]Unit{}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		unitDir := filepath.Join(root, name)
		descPath := filepath.Join(unitDir, "descriptor.yaml")
		if _, err := os.Stat(descPath); err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("%s: descriptor.yaml is required", unitDir)
			}
			return nil, err
		}
		desc, err := load.PackageDescriptor(descPath)
		if err != nil {
			return nil, err
		}
		if desc.Metadata.Name != "" && desc.Metadata.Name != name {
			return nil, fmt.Errorf("%s declares metadata.name %q", descPath, desc.Metadata.Name)
		}
		if install && desc.Spec.Renderer != name {
			return nil, fmt.Errorf("%s: install descriptor renderer %q must match directory %q", descPath, desc.Spec.Renderer, name)
		}
		if err := validateProvisioningStyle(descPath, install, unitDir, desc.Spec); err != nil {
			return nil, err
		}
		if err := validateReadiness(descPath, desc.Spec.Readiness); err != nil {
			return nil, err
		}
		if out[name].Name != "" {
			return nil, fmt.Errorf("duplicate unit %q", name)
		}
		out[name] = Unit{Name: name, SourceDir: unitDir, Descriptor: desc.Spec}
	}
	return out, nil
}
