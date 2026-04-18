// Apply-wave computation: assigns gitups.io/apply-wave values to every
// ResolvedPackage based on the unified dependsOn DAG. Runs after the main
// resolve loop *and* the bindings pass so binding-synthesized edges
// participate. Controller-agnostic: render emits a neutral annotation and
// controller packages translate it via their own overlays.

package resolve

import (
	"fmt"
	"sort"
	"strings"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
)

// computeApplyWaves walks every ResolvedPackage in fp, topo-sorts by
// DependsOn (resolved through the same alias set as topoSort uses for refs),
// and writes the resulting wave back onto each ResolvedPackage.
func computeApplyWaves(fp *v1.FullProvision) error {
	n := len(fp.Spec.Packages)
	if n == 0 {
		return nil
	}

	byName := buildPackageAliasIndex(fp.Spec.Packages)

	indeg := make([]int, n)
	adj := make([][]int, n)
	for i := range fp.Spec.Packages {
		rp := &fp.Spec.Packages[i]
		seenEdge := map[int]bool{}
		for _, dep := range rp.DependsOn {
			sources, ok := byName[dep]
			if !ok {
				continue
			}
			for _, src := range sources {
				if src == i || seenEdge[src] {
					continue
				}
				seenEdge[src] = true
				adj[src] = append(adj[src], i)
				indeg[i]++
			}
		}
	}

	waves := make([]int, n)
	var ready []int
	for i := 0; i < n; i++ {
		if indeg[i] == 0 {
			ready = append(ready, i)
		}
	}
	sort.Ints(ready)
	visited := 0
	for len(ready) > 0 {
		i := ready[0]
		ready = ready[1:]
		visited++
		for _, j := range adj[i] {
			if waves[i]+1 > waves[j] {
				waves[j] = waves[i] + 1
			}
			indeg[j]--
			if indeg[j] == 0 {
				ready = append(ready, j)
			}
		}
		sort.Ints(ready)
	}
	if visited != n {
		var cyc []string
		for i, d := range indeg {
			if d > 0 {
				cyc = append(cyc, fp.Spec.Packages[i].Instance)
			}
		}
		return fmt.Errorf("apply-wave: dependency cycle involving: %v", cyc)
	}
	for i := range fp.Spec.Packages {
		fp.Spec.Packages[i].ApplyWave = waves[i]
	}
	return nil
}

// buildPackageAliasIndex mirrors topoSort's alias scheme but runs over the
// final ResolvedPackage list (which includes binding-synthesized units).
func buildPackageAliasIndex(pkgs []v1.ResolvedPackage) map[string][]int {
	byName := map[string][]int{}
	for i, rp := range pkgs {
		pkgName := packageNameFromTemplate(rp.Template)
		addRefAlias(byName, rp.Instance, i)
		switch rp.UnitType {
		case v1.UnitTypeInstall:
			addRefAlias(byName, pkgName+"/install", i)
			addRefAlias(byName, rp.PackageInstance+"/install", i)
			if rp.InstallMethod != "" {
				addRefAlias(byName, pkgName+"/install/"+rp.InstallMethod, i)
				addRefAlias(byName, rp.PackageInstance+"/install/"+rp.InstallMethod, i)
			}
		case v1.UnitTypeResource:
			addRefAlias(byName, pkgName+"/resources/"+rp.ResourceTemplate, i)
			addRefAlias(byName, rp.PackageInstance+"/resources/"+rp.ResourceTemplate, i)
			addRefAlias(byName, pkgName+"/resources/"+rp.ResourceTemplate+"/"+rp.ResourceName, i)
			addRefAlias(byName, rp.PackageInstance+"/resources/"+rp.ResourceTemplate+"/"+rp.ResourceName, i)
		}
	}
	return byName
}

// packageNameFromTemplate returns the trailing "<package>" segment of a
// catalog-qualified template "<source>/<package>". Falls back to the whole
// string if no slash is present.
func packageNameFromTemplate(template string) string {
	if idx := strings.LastIndex(template, "/"); idx >= 0 {
		return template[idx+1:]
	}
	return template
}
