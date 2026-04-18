package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestApplyUsesToContextExactly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake kubectl script uses /bin/sh")
	}

	for _, contextName := range []string{"dsv", "kind-dsv"} {
		t.Run(contextName, func(t *testing.T) {
			tmp := t.TempDir()
			workspaceRoot := filepath.Join(tmp, "dsv")
			if err := os.MkdirAll(filepath.Join(workspaceRoot, "fake-repo"), 0o755); err != nil {
				t.Fatal(err)
			}
			fullProvision := `apiVersion: gitups/v1alpha1
kind: FullProvision
metadata:
  name: dsv
spec:
  sourceProvisionRef:
    name: dsv
  sources: []
  repository:
    layout: split
    outputPath: .
  packages:
    - template: local/noop
      packageInstance: noop
      unitType: install
      installMethod: raw
      repository: fake-repo
      instance: noop
      role: workload
      renderer: raw
      resolvedValues: {}
      renderedPaths:
        repo: fake-repo
        dir: packages/noop/install/raw
  placeholders: []
`
			if err := os.WriteFile(filepath.Join(workspaceRoot, "full-provision.yaml"), []byte(fullProvision), 0o644); err != nil {
				t.Fatal(err)
			}

			binDir := filepath.Join(tmp, "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			argsPath := filepath.Join(tmp, "kubectl-args")
			kubectl := filepath.Join(binDir, "kubectl")
			if err := os.WriteFile(kubectl, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$GITUPS_FAKE_KUBECTL_ARGS\"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("GITUPS_FAKE_KUBECTL_ARGS", argsPath)

			cmd := newApplyCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"dsv", "--output-dir", tmp, "--to", contextName})

			if err := cmd.Execute(); err != nil {
				t.Fatalf("apply command failed: %v\n%s", err, out.String())
			}
			body, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatal(err)
			}
			args := strings.Split(strings.TrimSpace(string(body)), "\n")
			if len(args) < 2 || args[0] != "--context" || args[1] != contextName {
				t.Fatalf("kubectl context args = %#v, want --context %q", args, contextName)
			}
		})
	}
}
