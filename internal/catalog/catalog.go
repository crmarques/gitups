// Package catalog resolves package sources to PackageDefinitions. v0.1 only
// supports the filesystem source driver.
package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
	"github.com/crmarques/gitups/internal/load"
)

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
		if out[name].Name != "" {
			return nil, fmt.Errorf("duplicate unit %q", name)
		}
		out[name] = Unit{Name: name, SourceDir: unitDir, Descriptor: desc.Spec}
	}
	return out, nil
}
