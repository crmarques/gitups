// Script-style resources: gitups owns the on-cluster shape. For every
// resource template with `provisioningStyle: script` we emit a deterministic
// Job, a ConfigMap carrying the package-authored scripts, and a ConfigMap
// carrying the resolved values as /etc/gitups/values.json. Package authors
// only write the script body — everything else below is gitups-owned.

package render

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
)

const (
	scriptsDir         = "scripts"
	provisionScript    = "provision.sh"
	teardownScript     = "teardown.sh"
	valuesMountPath    = "/etc/gitups"
	scriptsMountPath   = "/opt/gitups/scripts"
	valuesFileName     = "values.json"
	labelManagedBy     = "app.kubernetes.io/managed-by"
	managedByGitups    = "gitups"
	labelComponent     = "app.kubernetes.io/component"
	componentProvision = "script-provisioner"
	labelInstance      = "app.kubernetes.io/instance"
)

// renderScript writes the ConfigMap(s) + Job manifest trio into pkgDir.
// namespace lives in rp.ResolvedValues["namespace"] — package authors must
// declare a `namespace` input in the descriptor.
func renderScript(rp *v1.ResolvedPackage, unit renderUnit, pkgDir string) error {
	namespace, err := requireStringValue(rp.ResolvedValues, "namespace")
	if err != nil {
		return fmt.Errorf("script-style resource %s: %w", rp.Instance, err)
	}

	scripts, err := readScripts(filepath.Join(unit.SourceDir, scriptsDir))
	if err != nil {
		return err
	}
	if _, ok := scripts[provisionScript]; !ok {
		return fmt.Errorf("script-style resource %s: scripts/provision.sh missing", rp.Instance)
	}

	valuesJSON, err := json.MarshalIndent(rp.ResolvedValues, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal values: %w", err)
	}

	labels := map[string]string{
		labelManagedBy: managedByGitups,
		labelComponent: componentProvision,
		labelInstance:  rp.Instance,
	}
	scriptsCM := configMap(rp.Instance+"-scripts", namespace, labels, scripts)
	valuesCM := configMap(rp.Instance+"-values", namespace, labels, map[string]string{valuesFileName: string(valuesJSON)})

	sa := unit.ScriptServiceAccount
	if sa == "" {
		sa = "default"
	}
	job := provisionJob(rp.Instance, namespace, labels, unit.ScriptImage, sa)

	writes := map[string]any{
		"configmap-scripts.yaml": scriptsCM,
		"configmap-values.yaml":  valuesCM,
		"job-provision.yaml":     job,
	}
	names := make([]string, 0, len(writes))
	for n := range writes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		body, err := yaml.Marshal(writes[n])
		if err != nil {
			return fmt.Errorf("marshal %s: %w", n, err)
		}
		if err := os.WriteFile(filepath.Join(pkgDir, n), body, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", n, err)
		}
	}
	return nil
}

func readScripts(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read scripts dir %s: %w", dir, err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sh") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		out[e.Name()] = string(body)
	}
	return out, nil
}

func configMap(name, namespace string, labels map[string]string, data map[string]string) map[string]any {
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"labels":    cloneStringMap(labels),
		},
		"data": stringMapToAny(data),
	}
}

func provisionJob(instance, namespace string, labels map[string]string, image, serviceAccount string) map[string]any {
	podLabels := cloneStringMap(labels)
	return map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name":      instance + "-provision",
			"namespace": namespace,
			"labels":    cloneStringMap(labels),
		},
		"spec": map[string]any{
			"backoffLimit": 4,
			"template": map[string]any{
				"metadata": map[string]any{"labels": podLabels},
				"spec": map[string]any{
					"restartPolicy":      "OnFailure",
					"serviceAccountName": serviceAccount,
					"containers": []any{
						map[string]any{
							"name":            "provision",
							"image":           image,
							"imagePullPolicy": "IfNotPresent",
							"command":         []any{"/bin/sh", "-c"},
							"args":            []any{fmt.Sprintf("set -eu && cp %s/%s /tmp/provision.sh && chmod +x /tmp/provision.sh && /tmp/provision.sh", scriptsMountPath, provisionScript)},
							"env": []any{
								map[string]any{"name": "GITUPS_VALUES_FILE", "value": valuesMountPath + "/" + valuesFileName},
								map[string]any{"name": "GITUPS_INSTANCE", "value": instance},
								map[string]any{"name": "GITUPS_NAMESPACE", "value": namespace},
							},
							"volumeMounts": []any{
								map[string]any{"name": "scripts", "mountPath": scriptsMountPath, "readOnly": true},
								map[string]any{"name": "values", "mountPath": valuesMountPath, "readOnly": true},
							},
						},
					},
					"volumes": []any{
						map[string]any{
							"name": "scripts",
							"configMap": map[string]any{
								"name":        instance + "-scripts",
								"defaultMode": 0o555,
							},
						},
						map[string]any{
							"name": "values",
							"configMap": map[string]any{
								"name": instance + "-values",
							},
						},
					},
				},
			},
		},
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func stringMapToAny(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func requireStringValue(values map[string]any, key string) (string, error) {
	v, ok := values[key]
	if !ok {
		return "", fmt.Errorf("resolvedValues.%s is required for script-style resources", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("resolvedValues.%s must be a string, got %T", key, v)
	}
	if s == "" {
		return "", fmt.Errorf("resolvedValues.%s is empty", key)
	}
	return s, nil
}
