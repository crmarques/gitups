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
//
// KubeContext / ManifestPath / Namespace are shared with SRC CLI
// invocations. Kind / Name / Selector / Condition / Timeout / Pod are
// KRC-probe-specific: the KRC's spec.cli.intents templates reference
// them when rendering kubectl-equivalent commands for apply, get, wait,
// log tail, etc.
type CLIContext struct {
	KubeContext  string
	ManifestPath string
	Namespace    string
	Kind         string
	Name         string
	Selector     string
	Condition    string
	Timeout      string
	Pod          string
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
	return renderArgTemplates("cli.args", spec.Args, ctx)
}

// renderArgTemplates is the shared template-render helper for any args
// slice (spec.cli.args, spec.cli.intents[k].args). Returns one rendered
// string per input entry.
func renderArgTemplates(label string, args []string, ctx CLIContext) ([]string, error) {
	out := make([]string, 0, len(args))
	for i, a := range args {
		tmpl, err := template.New(fmt.Sprintf("%s[%d]", label, i)).Option("missingkey=error").Parse(a)
		if err != nil {
			return nil, fmt.Errorf("parse %s[%d] %q: %w", label, i, a, err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, ctx); err != nil {
			return nil, fmt.Errorf("render %s[%d] %q: %w", label, i, a, err)
		}
		out = append(out, buf.String())
	}
	return out, nil
}

// RenderIntentArgs renders one intent's args template against ctx.
// Separate entrypoint so callers do not need to synthesise a new
// ControllerCLI just to reuse RenderCLIArgs.
func RenderIntentArgs(intent string, args []string, ctx CLIContext) ([]string, error) {
	return renderArgTemplates("cli.intents."+intent+".args", args, ctx)
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
