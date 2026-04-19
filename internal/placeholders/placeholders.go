// Package placeholders defines the sentinel string for unfilled values and
// utilities to locate them inside a resolvedValues tree.
package placeholders

import (
	"fmt"
	"sort"
	"strings"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
)

// Sentinel is re-exported for callers that don't want to depend on the api
// package directly.
const Sentinel = v1.PlaceholderSentinel

// Contains reports whether v holds the sentinel string anywhere in its tree.
func Contains(v any) bool {
	switch t := v.(type) {
	case string:
		return strings.Contains(t, Sentinel)
	case map[string]any:
		for _, child := range t {
			if Contains(child) {
				return true
			}
		}
	case []any:
		for _, child := range t {
			if Contains(child) {
				return true
			}
		}
	}
	return false
}

// Scan walks resolvedValues for an instance and appends a Placeholder entry
// for every sentinel string found. Paths use the shape
// `spec.packages[<instance>].resolvedValues.<dotted.path>`; array indices are
// appended as `[n]`. Output is sorted by path for determinism.
func Scan(instance string, values map[string]any, reasons map[string]string, sensitive map[string]bool, generators map[string]*v1.Generator) []v1.Placeholder {
	root := fmt.Sprintf("spec.packages[%s].resolvedValues", instance)
	var out []v1.Placeholder
	walk(root, values, &out, reasons, sensitive, generators)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func walk(prefix string, v any, out *[]v1.Placeholder, reasons map[string]string, sensitive map[string]bool, generators map[string]*v1.Generator) {
	switch t := v.(type) {
	case string:
		if t == Sentinel {
			key := stripRoot(prefix)
			reason, sens, gen := lookup(key, reasons, sensitive, generators)
			if reason == "" {
				reason = "unfilled placeholder"
			}
			*out = append(*out, v1.Placeholder{Path: prefix, Reason: reason, Sensitive: sens, Generator: gen})
		}
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walk(prefix+"."+k, t[k], out, reasons, sensitive, generators)
		}
	case []any:
		for i, child := range t {
			walk(fmt.Sprintf("%s[%d]", prefix, i), child, out, reasons, sensitive, generators)
		}
	}
}

// lookup finds reason/sensitive/generator metadata for a path by trying
// exact match first and then progressively shorter prefixes (trimming
// bracket indices and dotted segments). Lets input authors annotate a
// top-level input like "addressPools" even when the actual sentinel
// lives at addressPools[0].cidrs[0]. Generators only resolve at exact
// or prefix match — they do not need a reason to be present.
func lookup(key string, reasons map[string]string, sensitive map[string]bool, generators map[string]*v1.Generator) (string, bool, *v1.Generator) {
	if r, ok := reasons[key]; ok {
		return r, sensitive[key], generators[key]
	}
	if g, ok := generators[key]; ok {
		return "", sensitive[key], g
	}
	cur := key
	for cur != "" {
		// trim trailing [n]
		if i := strings.LastIndexByte(cur, '['); i >= 0 && strings.HasSuffix(cur, "]") {
			cur = cur[:i]
			if r, ok := reasons[cur]; ok {
				return r, sensitive[cur], generators[cur]
			}
			if g, ok := generators[cur]; ok {
				return "", sensitive[cur], g
			}
			continue
		}
		// trim trailing .segment
		if i := strings.LastIndexByte(cur, '.'); i >= 0 {
			cur = cur[:i]
			if r, ok := reasons[cur]; ok {
				return r, sensitive[cur], generators[cur]
			}
			if g, ok := generators[cur]; ok {
				return "", sensitive[cur], g
			}
			continue
		}
		break
	}
	return "", false, nil
}

func stripRoot(path string) string {
	// find ".resolvedValues." and return the remainder
	const marker = ".resolvedValues."
	idx := strings.Index(path, marker)
	if idx < 0 {
		return path
	}
	return path[idx+len(marker):]
}
