// Service Resource Controller CLI execution. When gitups apply routes a
// unit through an SRC (e.g. declarest), it renders the controller's
// declared `spec.cli.args` against a fixed template context and execs the
// binary. Shape mirrors HelmRunner / KustomizeRunner so tests can stub it
// with a fake CLIRunner.

package cluster

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"text/template"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
)

// CLIRunner executes an arbitrary CLI invocation. Real implementations
// shell out; tests inject a recorder.
type CLIRunner interface {
	Run(ctx context.Context, binary string, args []string, stdout, stderr io.Writer) error
}

// CLIContext is the fixed template context exposed to ControllerCLI args.
// Packages may only reference these fields (plus literal strings); any
// other template variable is a render-time error. Keeping the surface
// small keeps invocations deterministic and auditable.
type CLIContext struct {
	KubeContext  string
	ManifestPath string
	Namespace    string
}

// RenderCLIArgs substitutes the declared CLI args against ctx using Go's
// text/template with `missingkey=error` so typos fail loudly instead of
// silently evaluating to "<no value>".
func RenderCLIArgs(spec *v1.ControllerCLI, ctx CLIContext) ([]string, error) {
	if spec == nil {
		return nil, fmt.Errorf("controller cli spec is nil")
	}
	if spec.Binary == "" {
		return nil, fmt.Errorf("controller cli binary is empty")
	}
	out := make([]string, 0, len(spec.Args))
	for i, a := range spec.Args {
		tmpl, err := template.New(fmt.Sprintf("cli.args[%d]", i)).Option("missingkey=error").Parse(a)
		if err != nil {
			return nil, fmt.Errorf("parse cli arg[%d] %q: %w", i, a, err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, ctx); err != nil {
			return nil, fmt.Errorf("render cli arg[%d] %q: %w", i, a, err)
		}
		out = append(out, buf.String())
	}
	return out, nil
}

// DefaultCLIRunner runs binary+args via os/exec, streaming stdout/stderr
// to the provided writers. Suitable for both shell and binary CLIs.
type DefaultCLIRunner struct{}

// Run implements CLIRunner.
func (DefaultCLIRunner) Run(ctx context.Context, binary string, args []string, stdout, stderr io.Writer) error {
	c := exec.CommandContext(ctx, binary, args...)
	c.Stdout = stdout
	c.Stderr = stderr
	return c.Run()
}
