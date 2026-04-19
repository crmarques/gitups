package secrets

import (
	"fmt"
	"strconv"
	"strings"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
)

// FillResult records one placeholder that Fill replaced. Path matches
// the original Placeholder.Path; Kind is the generator kind that
// produced the value. Values themselves are deliberately omitted —
// callers must never log or persist them outside FullProvision.
type FillResult struct {
	Path string
	Kind string
}

// Fill walks fp.Spec.Placeholders, generates a value for every entry
// whose Generator is set, writes it into the matching ResolvedValues
// tree, and removes the entry from the placeholder list. Re-run safely:
// already-filled paths simply have no Placeholder entry to act on.
//
// Errors short-circuit and leave the FullProvision in whatever state
// the caller passed in — callers should marshal back to disk only on a
// nil error.
func Fill(fp *v1.FullProvision) ([]FillResult, error) {
	pkgIdx := map[string]int{}
	for i := range fp.Spec.Packages {
		pkgIdx[fp.Spec.Packages[i].Instance] = i
	}

	var filled []FillResult
	var remaining []v1.Placeholder
	for _, ph := range fp.Spec.Placeholders {
		if ph.Generator == nil {
			remaining = append(remaining, ph)
			continue
		}
		instance, rest, err := splitPlaceholderPath(ph.Path)
		if err != nil {
			return nil, err
		}
		i, ok := pkgIdx[instance]
		if !ok {
			return nil, fmt.Errorf("placeholder %q: package instance %q not found", ph.Path, instance)
		}
		val, err := Generate(*ph.Generator)
		if err != nil {
			return nil, fmt.Errorf("placeholder %q: %w", ph.Path, err)
		}
		if fp.Spec.Packages[i].ResolvedValues == nil {
			fp.Spec.Packages[i].ResolvedValues = map[string]any{}
		}
		if err := setAtPath(fp.Spec.Packages[i].ResolvedValues, rest, val); err != nil {
			return nil, fmt.Errorf("placeholder %q: %w", ph.Path, err)
		}
		filled = append(filled, FillResult{Path: ph.Path, Kind: ph.Generator.Kind})
	}
	fp.Spec.Placeholders = remaining
	return filled, nil
}

// splitPlaceholderPath parses paths shaped like
//
//	spec.packages[<instance>].resolvedValues.<key>...
//
// into the instance name and the remainder past ".resolvedValues.".
func splitPlaceholderPath(path string) (instance, rest string, err error) {
	const prefix = "spec.packages["
	if !strings.HasPrefix(path, prefix) {
		return "", "", fmt.Errorf("path %q: missing %q prefix", path, prefix)
	}
	close := strings.IndexByte(path[len(prefix):], ']')
	if close < 0 {
		return "", "", fmt.Errorf("path %q: missing closing bracket on instance", path)
	}
	instance = path[len(prefix) : len(prefix)+close]
	tail := path[len(prefix)+close+1:]
	const marker = ".resolvedValues."
	if !strings.HasPrefix(tail, marker) {
		return "", "", fmt.Errorf("path %q: missing %q segment", path, marker)
	}
	return instance, tail[len(marker):], nil
}

// setAtPath walks a sequence of `.key` and `[index]` tokens (with the
// leading `.` already stripped from the first key) and writes v at the
// final position. Maps and slices along the way must already exist with
// compatible types — placeholders are only emitted for paths the
// resolver already populated, so the structure is guaranteed.
func setAtPath(root map[string]any, path string, v any) error {
	tokens, err := tokenizePath(path)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		return fmt.Errorf("empty path")
	}
	var cur any = root
	for i, tok := range tokens {
		last := i == len(tokens)-1
		switch t := tok.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				return fmt.Errorf("path token %q: parent is not a map (%T)", t, cur)
			}
			if last {
				m[t] = v
				return nil
			}
			cur = m[t]
		case int:
			s, ok := cur.([]any)
			if !ok {
				return fmt.Errorf("path token [%d]: parent is not a slice (%T)", t, cur)
			}
			if t < 0 || t >= len(s) {
				return fmt.Errorf("path token [%d]: out of range (len=%d)", t, len(s))
			}
			if last {
				s[t] = v
				return nil
			}
			cur = s[t]
		}
	}
	return nil
}

// tokenizePath turns "addressPools[0].cidrs[0]" into ["addressPools", 0,
// "cidrs", 0]. Tokens are either string (map key) or int (slice index).
func tokenizePath(path string) ([]any, error) {
	var out []any
	for path != "" {
		// Pull next key segment up to the next '.' or '['.
		next := len(path)
		for i := 0; i < len(path); i++ {
			if path[i] == '.' || path[i] == '[' {
				next = i
				break
			}
		}
		if next > 0 {
			out = append(out, path[:next])
			path = path[next:]
		}
		switch {
		case path == "":
			// done
		case path[0] == '.':
			path = path[1:]
		case path[0] == '[':
			close := strings.IndexByte(path, ']')
			if close < 0 {
				return nil, fmt.Errorf("path %q: unterminated [", path)
			}
			idx, err := strconv.Atoi(path[1:close])
			if err != nil {
				return nil, fmt.Errorf("path %q: bad index: %w", path, err)
			}
			out = append(out, idx)
			path = path[close+1:]
			if path != "" && path[0] == '.' {
				path = path[1:]
			}
		}
	}
	return out, nil
}
