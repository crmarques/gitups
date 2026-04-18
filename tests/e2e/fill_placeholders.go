package main

import (
	"fmt"
	"os"

	"sigs.k8s.io/yaml"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
	"github.com/crmarques/gitups/internal/placeholders"
)

func main() {
	if len(os.Args) != 6 {
		fmt.Fprintf(os.Stderr, "usage: fill_placeholders <full-provision.yaml> <env> <metallb-pool> <gitops-repo-url> <vault-root-token>\n")
		os.Exit(2)
	}
	path := os.Args[1]
	env := os.Args[2]
	metallbPool := os.Args[3]
	gitopsRepoURL := os.Args[4]
	vaultRootToken := os.Args[5]

	raw, err := os.ReadFile(path)
	if err != nil {
		die("read %s: %v", path, err)
	}
	var fp v1.FullProvision
	if err := yaml.Unmarshal(raw, &fp); err != nil {
		die("parse %s: %v", path, err)
	}

	for i := range fp.Spec.Packages {
		rp := &fp.Spec.Packages[i]
		switch rp.Instance {
		case "metallb-config-default":
			rp.ResolvedValues["addressPools"] = []any{
				map[string]any{
					"name":  "default",
					"cidrs": []any{metallbPool},
				},
			}
		case "vault":
			server, _ := rp.ResolvedValues["server"].(map[string]any)
			if server == nil {
				server = map[string]any{}
				rp.ResolvedValues["server"] = server
			}
			dev, _ := server["dev"].(map[string]any)
			if dev == nil {
				dev = map[string]any{}
				server["dev"] = dev
			}
			dev["devRootToken"] = vaultRootToken
		case "argocd-applications-root", "declarest-config-default":
			rp.ResolvedValues["repoURL"] = gitopsRepoURL
		case "service-mesh-config-default":
			rp.ResolvedValues["meshID"] = "mesh-" + env
		}
	}

	var remaining []string
	for _, rp := range fp.Spec.Packages {
		if placeholders.Contains(rp.ResolvedValues) {
			remaining = append(remaining, rp.Instance)
		}
	}
	if len(remaining) > 0 {
		die("unfilled placeholders remain in packages: %v", remaining)
	}
	fp.Spec.Placeholders = []v1.Placeholder{}

	body, err := yaml.Marshal(&fp)
	if err != nil {
		die("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		die("write %s: %v", path, err)
	}
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fill_placeholders: "+format+"\n", args...)
	os.Exit(1)
}
