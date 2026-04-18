package v1alpha1_test

import (
	"reflect"
	"testing"

	"sigs.k8s.io/yaml"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
)

func TestProvisionRoundTrip(t *testing.T) {
	in := v1.Provision{
		APIVersion: v1.APIVersion,
		Kind:       v1.KindProvision,
		Metadata:   v1.ObjectMeta{Name: "dsv"},
		Spec: v1.ProvisionSpec{
			Sources: []v1.PackageSource{
				{Name: "local", Type: "filesystem", Path: "../gitups-packages"},
			},
			Repositories: []v1.RepositoryDecl{
				{
					Name: "base",
					Type: "k8s-gitops-generic",
					Packages: []v1.PackageRef{
						{Template: "local/argocd"},
						{Template: "local/metallb", InstallMethod: "helm"},
					},
				},
				{
					Name: "base-{{.Env}}",
					Type: "k8s-gitops-env",
					RepoRef: &v1.RepositoryRef{
						Name:   "base",
						Commit: "v0.0.1",
					},
					Packages: []v1.PackageRef{
						{
							Template: "local/metallb",
							Resources: []v1.ResourceRef{
								{Template: "config", Name: "pool-a"},
							},
						},
					},
				},
			},
		},
	}

	buf, err := yaml.Marshal(&in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out v1.Provision
	if err := yaml.Unmarshal(buf, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round-trip mismatch:\n in=%#v\nout=%#v", in, out)
	}
}

func TestFullProvisionRoundTrip(t *testing.T) {
	in := v1.FullProvision{
		APIVersion: v1.APIVersion,
		Kind:       v1.KindFullProvision,
		Metadata:   v1.ObjectMeta{Name: "dsv"},
		Spec: v1.FullProvisionSpec{
			SourceProvisionRef: v1.ObjectMeta{Name: "dsv"},
			Sources: []v1.PackageSource{
				{Name: "local", Type: "filesystem", Path: "../gitups-packages"},
			},
			Repository: v1.RepositoryBlock{Layout: "split", OutputPath: "./out/dsv"},
			Packages: []v1.ResolvedPackage{
				{
					Template:        "local/argocd",
					PackageInstance: "argocd",
					UnitType:        v1.UnitTypeInstall,
					InstallMethod:   "olm",
					Repository:      "base",
					Instance:        "argocd",
					Role:            "controller",
					Renderer:        "olm",
					ResolvedValues: map[string]any{
						"namespace": "argocd",
						"repoURL":   v1.PlaceholderSentinel,
					},
					RenderedPaths: v1.RenderedPaths{Repo: "base", Dir: "packages/argocd/install/olm"},
				},
			},
			Placeholders: []v1.Placeholder{
				{Path: "spec.packages[argocd].resolvedValues.repoURL", Reason: "base repo URL"},
			},
		},
	}

	buf, err := yaml.Marshal(&in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out v1.FullProvision
	if err := yaml.Unmarshal(buf, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round-trip mismatch:\n in=%#v\nout=%#v", in, out)
	}
}

func TestPackageDefinitionRoundTrip(t *testing.T) {
	in := v1.PackageDefinition{
		APIVersion: v1.APIVersion,
		Kind:       v1.KindPackageDefinition,
		Metadata:   v1.PackageMeta{Name: "nginx-ingress", Version: "0.0.1"},
		Spec: v1.PackageDefinitionSpec{
			Role:           "workload",
			Category:       "ingress",
			DefaultInstall: "helm",
		},
	}
	buf, err := yaml.Marshal(&in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out v1.PackageDefinition
	if err := yaml.Unmarshal(buf, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round-trip mismatch:\n in=%#v\nout=%#v", in, out)
	}
}

func TestPackageDescriptorRoundTrip(t *testing.T) {
	in := v1.PackageDescriptor{
		APIVersion: v1.APIVersion,
		Kind:       v1.KindPackageDescriptor,
		Metadata:   v1.ObjectMeta{Name: "helm"},
		Spec: v1.PackageDescriptorSpec{
			Renderer: v1.RendererHelm,
			Helm: &v1.HelmSpec{
				Repo:           "https://kubernetes.github.io/ingress-nginx",
				Chart:          "ingress-nginx",
				Version:        "4.11.3",
				ValuesTemplate: "values.yaml.tmpl",
			},
			Inputs: []v1.InputSpec{
				{Name: "namespace", Type: "string", Default: "ingress-nginx"},
			},
			DependsOn: []string{"metallb/install"},
		},
	}
	buf, err := yaml.Marshal(&in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out v1.PackageDescriptor
	if err := yaml.Unmarshal(buf, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round-trip mismatch:\n in=%#v\nout=%#v", in, out)
	}
}
