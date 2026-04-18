package render

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"sigs.k8s.io/yaml"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
)

// HelmTemplateRequest carries all inputs needed to produce install.yaml for a
// Helm-rendered package.
type HelmTemplateRequest struct {
	Instance   string
	Chart      string
	Repo       string
	Version    string
	Namespace  string
	ValuesFile string // absolute path to the rendered values.yaml
}

// HelmRunner abstracts helm invocation so tests can stub it.
type HelmRunner interface {
	Template(ctx context.Context, req HelmTemplateRequest) (string, error)
}

// ExecHelmRunner shells out to the `helm` binary.
type ExecHelmRunner struct {
	Bin string
}

func NewExecHelmRunner(bin string) *ExecHelmRunner {
	if bin == "" {
		bin = "helm"
	}
	return &ExecHelmRunner{Bin: bin}
}

func (r *ExecHelmRunner) Template(ctx context.Context, req HelmTemplateRequest) (string, error) {
	args := []string{
		"template", req.Instance, req.Chart,
		"--repo", req.Repo,
		"--version", req.Version,
		"--namespace", req.Namespace,
		"--include-crds",
		"-f", req.ValuesFile,
	}
	cmd := exec.CommandContext(ctx, r.Bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("helm template: %w: %s", err, string(out))
	}
	return string(out), nil
}

func renderHelm(ctx context.Context, rp *v1.ResolvedPackage, unit renderUnit, pkgDir string, tctx templateCtx, runner HelmRunner) error {
	hs := unit.Helm
	if hs == nil {
		return fmt.Errorf("renderer=helm but spec.helm is nil")
	}

	// 1. produce values.yaml — either via the package's values template, or by
	//    marshalling resolvedValues directly if no template is set.
	var valuesBody []byte
	if hs.ValuesTemplate != "" {
		tmplPath := filepath.Join(unit.SourceDir, hs.ValuesTemplate)
		rendered, err := renderTemplateFile(tmplPath, tctx)
		if err != nil {
			return fmt.Errorf("values template: %w", err)
		}
		valuesBody = []byte(rendered)
	} else {
		body, err := yaml.Marshal(rp.ResolvedValues)
		if err != nil {
			return fmt.Errorf("marshal values: %w", err)
		}
		valuesBody = body
	}
	valuesPath := filepath.Join(pkgDir, "values.yaml")
	if err := os.WriteFile(valuesPath, valuesBody, 0o644); err != nil {
		return fmt.Errorf("write values.yaml: %w", err)
	}

	// 2. run helm template → install.yaml.
	namespace, _ := rp.ResolvedValues["namespace"].(string)
	if namespace == "" {
		namespace = "default"
	}
	req := HelmTemplateRequest{
		Instance:   rp.Instance,
		Chart:      hs.Chart,
		Repo:       hs.Repo,
		Version:    resolveHelmVersion(hs.Version, rp.ResolvedValues),
		Namespace:  namespace,
		ValuesFile: valuesPath,
	}
	out, err := runner.Template(ctx, req)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(pkgDir, "install.yaml"), []byte(out), 0o644)
}

func resolveHelmVersion(defaultVersion string, values map[string]any) string {
	chart, _ := values["chart"].(map[string]any)
	if version, _ := chart["version"].(string); version != "" {
		return version
	}
	return defaultVersion
}
