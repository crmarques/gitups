package render_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/crmarques/gitups/internal/catalog"
	"github.com/crmarques/gitups/internal/load"
	"github.com/crmarques/gitups/internal/render"
)

// stubHelmRunner returns a canned install.yaml per chart so tests don't shell
// out to the real `helm` binary. Output is stable across runs (hermetic) and
// small enough to diff.
type stubHelmRunner struct{}

var stubHelmOutputs = map[string]string{
	"argo-cd":       "# stub-helm: argo-cd\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: argocd-server\nspec:\n  replicas: 1\n",
	"metallb":       "# stub-helm: metallb\napiVersion: apps/v1\nkind: DaemonSet\nmetadata:\n  name: metallb-speaker\n",
	"ingress-nginx": "# stub-helm: ingress-nginx\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: ingress-nginx-controller\n",
	"gitea":         "# stub-helm: gitea\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: gitea\n",
	"vault":         "# stub-helm: vault\napiVersion: apps/v1\nkind: StatefulSet\nmetadata:\n  name: vault\n",
	"base":          "# stub-helm: istio-base\napiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: virtualservices.networking.istio.io\n",
	"istiod":        "# stub-helm: istiod\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: istiod\n",
}

func (stubHelmRunner) Template(ctx context.Context, req render.HelmTemplateRequest) (string, error) {
	body, ok := stubHelmOutputs[req.Chart]
	if !ok {
		return "", fmt.Errorf("stub helm: no canned output for chart %q", req.Chart)
	}
	header := fmt.Sprintf("# stub: instance=%s chart=%s version=%s namespace=%s\n", req.Instance, req.Chart, req.Version, req.Namespace)
	return header + body, nil
}

// TestRenderRuns drives Render end-to-end against the pinned
// full-provision-dsv-filled.yaml fixture and the sibling gitups-packages
// catalog. It asserts only that render completes; output shape is covered by
// per-renderer unit tests so the fixture doesn't have to track every chart
// stub byte-for-byte.
func TestRenderRuns(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	fullProvPath := filepath.Join(repoRoot, "tests/e2e/full-provision-dsv-filled.yaml")
	fp, err := load.FullProvision(fullProvPath)
	if err != nil {
		t.Fatalf("load full-provision: %v", err)
	}
	baseDir := filepath.Dir(fullProvPath)
	cat, err := catalog.Build(fp.Spec.Sources, baseDir)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}

	outDir := filepath.Join(t.TempDir(), "dsv")
	err = render.Render(context.Background(), fp, cat, render.Options{
		OutputPath:     outDir,
		KubectlContext: "dsv",
		Helm:           stubHelmRunner{},
		SourceFullProv: fullProvPath,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
}
