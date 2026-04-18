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
// it was loaded from (needed later by the renderer to find install/resource
// overlays, raw manifests, and scripts).
type Entry struct {
	Source     string // source name (e.g. "local")
	SourceRoot string // absolute path to the source root
	Dir        string // absolute path to the package dir
	Def        *v1.PackageDefinition
	Installs   map[string]Unit
	Resources  map[string]Unit
}

type Unit struct {
	Name       string
	SourceDir  string
	Descriptor v1.PackageDescriptorSpec
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
		installs, err := loadUnits(filepath.Join(pkgDir, "install"), true)
		if err != nil {
			return fmt.Errorf("source %q: package %s: %w", name, def.Metadata.Name, err)
		}
		resources, err := loadUnits(filepath.Join(pkgDir, "resources"), false)
		if err != nil {
			return fmt.Errorf("source %q: package %s: %w", name, def.Metadata.Name, err)
		}
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
			Installs:   installs,
			Resources:  resources,
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
