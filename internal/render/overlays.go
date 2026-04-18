package render

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"sigs.k8s.io/yaml"
)

// templateCtx is the root object passed to every text/template rendered by
// gitups (helm values templates, overlays, kustomize value overlays). Dotted
// access `.Values.foo.bar` pulls from resolvedValues; `.Instance`, `.Env`, and
// `.Context` are convenience fields.
type templateCtx struct {
	Values           map[string]any
	Instance         string
	PackageInstance  string
	UnitType         string
	ResourceTemplate string
	ResourceName     string
	Env              string
	Context          string
}

// funcMap is the minimal helper set exposed to package authors. Kept small on
// purpose — packages should derive most shaping from resolvedValues rather
// than template logic.
var funcMap = template.FuncMap{
	"toYaml": func(v any) (string, error) {
		b, err := yaml.Marshal(v)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(b), "\n"), nil
	},
	"indent": func(n int, s string) string {
		pad := strings.Repeat(" ", n)
		return pad + strings.ReplaceAll(s, "\n", "\n"+pad)
	},
	"nindent": func(n int, s string) string {
		pad := strings.Repeat(" ", n)
		return "\n" + pad + strings.ReplaceAll(s, "\n", "\n"+pad)
	},
	"default": func(def, v any) any {
		if v == nil {
			return def
		}
		if s, ok := v.(string); ok && s == "" {
			return def
		}
		return v
	},
}

func renderTemplateString(name, body string, ctx templateCtx) (string, error) {
	tmpl, err := template.New(name).Funcs(funcMap).Option("missingkey=error").Parse(body)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("execute %s: %w", name, err)
	}
	return buf.String(), nil
}

func renderTemplateFile(path string, ctx templateCtx) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return renderTemplateString(filepath.Base(path), string(body), ctx)
}

// renderOverlays walks each configured overlays directory and writes templates
// text/template into pkgDir with the .tmpl suffix stripped.
func renderOverlays(unit renderUnit, pkgDir string, tctx templateCtx) error {
	overlays := unit.Overlays
	if len(overlays) == 0 {
		overlays = []string{"overlays"}
	}
	for _, overlay := range overlays {
		src := filepath.Join(unit.SourceDir, overlay)
		info, err := os.Stat(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if !info.IsDir() {
			continue
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if !strings.HasSuffix(e.Name(), ".tmpl") {
				continue
			}
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, n := range names {
			body, err := renderTemplateFile(filepath.Join(src, n), tctx)
			if err != nil {
				return err
			}
			dst := filepath.Join(pkgDir, strings.TrimSuffix(n, ".tmpl"))
			if err := os.WriteFile(dst, []byte(body), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}
