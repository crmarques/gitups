package load_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crmarques/gitups/internal/load"
)

func TestProvisionLoads(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	p, err := load.Provision(filepath.Join(repoRoot, "tests/e2e/dsv/provision.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if p.Metadata.Name != "dsv" {
		t.Errorf("metadata.name: got %q", p.Metadata.Name)
	}
	if len(p.Spec.Repositories) != 6 {
		t.Errorf("want 6 repositories, got %d", len(p.Spec.Repositories))
	}
}

func TestFullProvisionLoads(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	fp, err := load.FullProvision(filepath.Join(repoRoot, "tests/e2e/dsv/full-provision-filled.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if fp.Spec.Repository.Layout != "split" {
		t.Errorf("layout: got %q", fp.Spec.Repository.Layout)
	}
}

func TestDetectKind(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tm, err := load.DetectKind(filepath.Join(repoRoot, "tests/e2e/dsv/provision.yaml"))
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if tm.Kind != "Provision" {
		t.Errorf("kind: got %q", tm.Kind)
	}
}

// TestProvisionResolvedNoExtends exercises the back-compat path: a Provision
// without spec.extends must load identically through Provision and
// ProvisionResolved, with a nil ExtendedFrom trace.
func TestProvisionResolvedNoExtends(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	path := filepath.Join(repoRoot, "tests/e2e/dsv/provision.yaml")
	p, extFrom, err := load.ProvisionResolved(path)
	if err != nil {
		t.Fatalf("resolved: %v", err)
	}
	if extFrom != nil {
		t.Errorf("want nil ExtendedFrom, got %+v", extFrom)
	}
	if p.Metadata.Name != "dsv" {
		t.Errorf("metadata.name: got %q", p.Metadata.Name)
	}
	if len(p.Spec.Repositories) != 6 {
		t.Errorf("want 6 repositories, got %d", len(p.Spec.Repositories))
	}
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

// TestProvisionResolvedExtendsMerge covers the core extends behavior: env
// picks up base sources and repositories, merges values on matching packages,
// appends env-only packages, and records ExtendedFrom.
func TestProvisionResolvedExtendsMerge(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "basic-infra/provision.yaml", `apiVersion: gitups/v1alpha1
kind: Provision
metadata:
  name: basic-infra
spec:
  sources:
    - name: local
      type: filesystem
      path: ./pkgs
  repositories:
    - name: base
      type: k8s-gitops-generic
      packages:
        - template: local/metallb
        - template: local/argocd
`)
	writeFile(t, dir, "basic-infra-dev/provision.yaml", `apiVersion: gitups/v1alpha1
kind: Provision
metadata:
  name: basic-infra-dev
spec:
  envKey: dev
  extends:
    source: ../basic-infra/provision.yaml
    ref: v1.4.0
  repositories:
    - name: base-{{.Env}}
      type: k8s-gitops-env
      repoRef:
        name: base
        commit: v0.0.1
`)
	merged, extFrom, err := load.ProvisionResolved(filepath.Join(dir, "basic-infra-dev/provision.yaml"))
	if err != nil {
		t.Fatalf("resolved: %v", err)
	}
	if extFrom == nil || extFrom.Source != "../basic-infra/provision.yaml" || extFrom.Ref != "v1.4.0" {
		t.Fatalf("ExtendedFrom: got %+v", extFrom)
	}
	if merged.Spec.Extends != nil {
		t.Errorf("effective Provision must not carry spec.extends, got %+v", merged.Spec.Extends)
	}
	if merged.Metadata.Name != "basic-infra-dev" {
		t.Errorf("metadata.name: got %q", merged.Metadata.Name)
	}
	if merged.Spec.EnvKey != "dev" {
		t.Errorf("envKey: got %q", merged.Spec.EnvKey)
	}
	if len(merged.Spec.Sources) != 1 || merged.Spec.Sources[0].Name != "local" {
		t.Fatalf("sources: got %+v", merged.Spec.Sources)
	}
	if len(merged.Spec.Repositories) != 2 {
		t.Fatalf("repositories: want 2, got %d (%+v)", len(merged.Spec.Repositories), merged.Spec.Repositories)
	}
	if merged.Spec.Repositories[0].Name != "base" || merged.Spec.Repositories[1].Name != "base-{{.Env}}" {
		t.Fatalf("unexpected repository order: %+v", merged.Spec.Repositories)
	}
}

// TestProvisionResolvedRejectsTransitiveExtends: a base Provision cannot
// itself declare spec.extends — we keep resolution to a single level so
// state is easy to reason about.
func TestProvisionResolvedRejectsTransitiveExtends(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a/provision.yaml", `apiVersion: gitups/v1alpha1
kind: Provision
metadata: {name: a}
spec:
  sources: [{name: local, type: filesystem, path: ./pkgs}]
  repositories:
    - name: base
      type: k8s-gitops-generic
      packages: [{template: local/x}]
`)
	writeFile(t, dir, "b/provision.yaml", `apiVersion: gitups/v1alpha1
kind: Provision
metadata: {name: b}
spec:
  extends: {source: ../a/provision.yaml}
  repositories:
    - name: env
      type: k8s-gitops-env
      repoRef: {name: base}
`)
	writeFile(t, dir, "c/provision.yaml", `apiVersion: gitups/v1alpha1
kind: Provision
metadata: {name: c}
spec:
  extends: {source: ../b/provision.yaml}
  repositories:
    - name: env2
      type: k8s-gitops-env
      repoRef: {name: base}
`)
	_, _, err := load.ProvisionResolved(filepath.Join(dir, "c/provision.yaml"))
	if err == nil {
		t.Fatal("expected transitive extends to error")
	}
	if !strings.Contains(err.Error(), "one level") {
		t.Errorf("error should mention single-level rule: %v", err)
	}
}

// TestProvisionResolvedRejectsGitSource defers git+ source support to a
// follow-up; today it must fail with a clear, user-facing error.
func TestProvisionResolvedRejectsGitSource(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "env/provision.yaml", `apiVersion: gitups/v1alpha1
kind: Provision
metadata: {name: env}
spec:
  extends:
    source: git+https://example.com/acme/gitops.git#basic-infra/provision.yaml
    ref: v1.0.0
  repositories: []
`)
	_, _, err := load.ProvisionResolved(filepath.Join(dir, "env/provision.yaml"))
	if err == nil {
		t.Fatal("expected git+ source to be rejected")
	}
	if !strings.Contains(err.Error(), "git+") {
		t.Errorf("error should mention git+ prefix: %v", err)
	}
}

// TestProvisionResolvedSourceConflict: a source name that appears in both
// base and env is ambiguous and must error rather than silently preferring
// one side's definition.
func TestProvisionResolvedSourceConflict(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "base/provision.yaml", `apiVersion: gitups/v1alpha1
kind: Provision
metadata: {name: base}
spec:
  sources: [{name: local, type: filesystem, path: ./pkgs}]
  repositories:
    - name: base
      type: k8s-gitops-generic
      packages: [{template: local/x}]
`)
	writeFile(t, dir, "env/provision.yaml", `apiVersion: gitups/v1alpha1
kind: Provision
metadata: {name: env}
spec:
  extends: {source: ../base/provision.yaml}
  sources: [{name: local, type: filesystem, path: ./other}]
  repositories: []
`)
	_, _, err := load.ProvisionResolved(filepath.Join(dir, "env/provision.yaml"))
	if err == nil {
		t.Fatal("expected source conflict to error")
	}
	if !strings.Contains(err.Error(), "local") {
		t.Errorf("error should name the conflicting source: %v", err)
	}
}

func TestProvisionRejectsOldTopLevelPackages(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "provision.yaml", `apiVersion: gitups/v1alpha1
kind: Provision
metadata: {name: dsv}
spec:
  sources: [{name: local, type: filesystem, path: ./pkgs}]
  packages: [{template: local/old}]
`)
	_, err := load.Provision(path)
	if err == nil {
		t.Fatal("expected old spec.packages to be rejected")
	}
	if !strings.Contains(err.Error(), "spec.packages") {
		t.Errorf("error should mention spec.packages: %v", err)
	}
}
