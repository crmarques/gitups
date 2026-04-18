package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
)

// renderOLM emits operator-group.yaml + subscription.yaml for an OLM-rendered
// package. The namespace manifest is expected from the package's
// overlays/namespace.yaml.tmpl (shared pattern across renderers).
//
// Package defaults come from package.yaml `spec.olm`. Per-instance overrides
// live under resolvedValues.olm.{channel, startingCSV, source, sourceNamespace,
// installPlanApproval, operatorGroupScope}.
func renderOLM(ctx context.Context, rp *v1.ResolvedPackage, unit renderUnit, pkgDir string, tctx templateCtx) error {
	_ = ctx
	_ = tctx
	spec := unit.OLM
	if spec == nil {
		return fmt.Errorf("renderer=olm but spec.olm is nil")
	}

	namespace, _ := rp.ResolvedValues["namespace"].(string)
	if namespace == "" {
		return fmt.Errorf("renderer=olm requires resolvedValues.namespace")
	}

	resolved, err := resolveOLMFields(spec, rp.ResolvedValues)
	if err != nil {
		return err
	}

	og := map[string]any{
		"apiVersion": "operators.coreos.com/v1",
		"kind":       "OperatorGroup",
		"metadata": map[string]any{
			"name":      rp.Instance + "-og",
			"namespace": namespace,
		},
	}
	switch resolved.OperatorGroupScope {
	case "AllNamespaces":
		og["spec"] = map[string]any{}
	case "OwnNamespace", "SingleNamespace":
		og["spec"] = map[string]any{"targetNamespaces": []any{namespace}}
	}
	if err := writeYAML(filepath.Join(pkgDir, "operator-group.yaml"), og); err != nil {
		return fmt.Errorf("write operator-group.yaml: %w", err)
	}

	sub := map[string]any{
		"apiVersion": "operators.coreos.com/v1alpha1",
		"kind":       "Subscription",
		"metadata": map[string]any{
			"name":      rp.Instance,
			"namespace": namespace,
		},
		"spec": map[string]any{
			"channel":             resolved.Channel,
			"name":                spec.Package,
			"source":              resolved.Source,
			"sourceNamespace":     resolved.SourceNamespace,
			"startingCSV":         resolved.StartingCSV,
			"installPlanApproval": resolved.InstallPlanApproval,
		},
	}
	if err := writeYAML(filepath.Join(pkgDir, "subscription.yaml"), sub); err != nil {
		return fmt.Errorf("write subscription.yaml: %w", err)
	}
	return nil
}

// olmResolved carries the package-spec defaults after per-instance overrides
// under resolvedValues.olm.* have been merged on top.
type olmResolved struct {
	Channel             string
	Source              string
	SourceNamespace     string
	StartingCSV         string
	InstallPlanApproval string
	OperatorGroupScope  string
}

func resolveOLMFields(spec *v1.OLMSpec, values map[string]any) (olmResolved, error) {
	r := olmResolved{
		Channel:             spec.Channel,
		Source:              spec.Source,
		SourceNamespace:     spec.SourceNamespace,
		StartingCSV:         spec.StartingCSV,
		InstallPlanApproval: spec.InstallPlanApproval,
		OperatorGroupScope:  spec.OperatorGroupScope,
	}
	overrides, _ := values["olm"].(map[string]any)
	if s, ok := overrides["channel"].(string); ok && s != "" {
		r.Channel = s
	}
	if s, ok := overrides["source"].(string); ok && s != "" {
		r.Source = s
	}
	if s, ok := overrides["sourceNamespace"].(string); ok && s != "" {
		r.SourceNamespace = s
	}
	if s, ok := overrides["startingCSV"].(string); ok && s != "" {
		r.StartingCSV = s
	}
	if s, ok := overrides["installPlanApproval"].(string); ok && s != "" {
		r.InstallPlanApproval = s
	}
	if s, ok := overrides["operatorGroupScope"].(string); ok && s != "" {
		r.OperatorGroupScope = s
	}
	if r.InstallPlanApproval == "" {
		r.InstallPlanApproval = "Automatic"
	}
	if r.OperatorGroupScope == "" {
		r.OperatorGroupScope = "OwnNamespace"
	}
	switch r.OperatorGroupScope {
	case "AllNamespaces", "OwnNamespace", "SingleNamespace":
	default:
		return r, fmt.Errorf("operatorGroupScope %q invalid (AllNamespaces | OwnNamespace | SingleNamespace)", r.OperatorGroupScope)
	}
	if r.InstallPlanApproval != "Automatic" && r.InstallPlanApproval != "Manual" {
		return r, fmt.Errorf("installPlanApproval %q invalid (Automatic | Manual)", r.InstallPlanApproval)
	}
	return r, nil
}

func writeYAML(path string, obj any) error {
	body, err := yaml.Marshal(obj)
	if err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}
