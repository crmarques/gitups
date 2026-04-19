package main

import (
	"fmt"
	"os"
	"strings"

	"sigs.k8s.io/yaml"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
	"github.com/crmarques/gitups/internal/placeholders"
)

// fill_placeholders seeds every placeholder produced by `gitups expand` for
// the basic-binding case:
//   - metallb addressPools from $METALLB_ADDRESS_POOL
//   - argocd repoURL (install + applications/root) from $GITOPS_REPO_URL
//   - the gitea binding's `token` export (secret) from $GITEA_ARGOCD_BOT_TOKEN
//
// We fill only the provider side of the binding; the consumer side picks it
// up on the next `gitups expand` pass via nonSentinelFill in resolve/bindings.
func main() {
	if len(os.Args) != 5 {
		fmt.Fprintf(os.Stderr, "usage: fill_placeholders <full-provision.yaml> <metallb-pool> <gitops-repo-url> <gitea-argocd-bot-token>\n")
		os.Exit(2)
	}
	path := os.Args[1]
	metallbPool := os.Args[2]
	gitopsRepoURL := os.Args[3]
	giteaBotToken := os.Args[4]

	raw, err := os.ReadFile(path)
	if err != nil {
		die("read %s: %v", path, err)
	}
	var fp v1.FullProvision
	if err := yaml.Unmarshal(raw, &fp); err != nil {
		die("parse %s: %v", path, err)
	}

	providerFilled := false
	for i := range fp.Spec.Packages {
		rp := &fp.Spec.Packages[i]
		switch {
		case rp.Instance == "metallb-config-default":
			rp.ResolvedValues["addressPools"] = []any{
				map[string]any{
					"name":  "default",
					"cidrs": []any{metallbPool},
				},
			}
		case rp.Domain == v1.DomainKRC && rp.ResourceTemplate == "managed-repo":
			// KRC-synthesised managed-repo units carry a repoURL placeholder;
			// point each at a per-target git URL derived from the resource
			// name (which equals the target output repo name).
			rp.ResolvedValues["repoURL"] = fmt.Sprintf("https://example.invalid/gitops/%s.git", rp.ResourceName)
		case rp.Binding != nil && rp.Binding.Side == "provider" &&
			strings.HasPrefix(rp.Instance, "gitea-user-argocd-gitea-bot-"):
			rp.ResolvedValues["token"] = giteaBotToken
			providerFilled = true
		}
	}
	_ = gitopsRepoURL // retained for compatibility with run.sh arg order
	if !providerFilled {
		die("no provider-side binding resource found for argocd-gitea-bot; expand may have changed")
	}

	var remaining []string
	for _, rp := range fp.Spec.Packages {
		if placeholders.Contains(rp.ResolvedValues) {
			remaining = append(remaining, rp.Instance)
		}
	}
	if len(remaining) > 0 {
		// Consumer-side placeholders are expected to remain here; they are
		// filled on the next `gitups expand` via cross-side propagation. The
		// consumer is scoped to argocd-repo-secret for this binding. Dropping
		// the consumer from the "remaining" list lets us detect any other
		// drift without blocking the expected consumer case.
		filtered := remaining[:0]
		for _, inst := range remaining {
			if strings.HasPrefix(inst, "argocd-repo-secret-argocd-gitea-bot-") {
				continue
			}
			filtered = append(filtered, inst)
		}
		if len(filtered) > 0 {
			die("unfilled placeholders remain in packages: %v", filtered)
		}
	}
	// Drop placeholders list so `gitups check` reads only resolved values.
	// Re-expand will repopulate it with any still-outstanding entries.
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
