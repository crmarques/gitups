// Package cluster — KubeClient is the gitups-core abstraction for every
// cluster operation. Gitups does not hard-code kubectl; it takes the
// selected KRC package's spec.cli block and routes every operation
// through a named intent on that block. The KRC is what decides what
// binary to call and with what args — argocd ships a kubectl-backed
// intent set.
//
// Intent names are `v1.Intent*` constants. An intent missing from the
// KRC's spec.cli.intents surfaces a clear error naming the intent; the
// user fixes the KRC package, not gitups core.

package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
)

// KubeClient wraps a KRC's spec.cli with one method per intent gitups
// uses. The binary and the args templates all come from the KRC's
// package.yaml; this type knows nothing kubectl-specific.
type KubeClient struct {
	cli         *v1.ControllerCLI
	kubeContext string
	runner      CLIRunner
	// krcName is the owning package's metadata.name — used only for
	// error messages that say "KRC package %q does not declare intent
	// %q".
	krcName string
}

// NewKubeClient builds a client from a KRC's spec.cli block. Callers
// look up the KRC via the Provision's spec.controllers.kubernetesResources
// assignment and the package catalog. kubeContext is the --to flag
// value; runner is injectable for tests.
func NewKubeClient(cli *v1.ControllerCLI, krcName, kubeContext string, runner CLIRunner) (*KubeClient, error) {
	if cli == nil {
		return nil, fmt.Errorf("KRC %q: spec.cli is not declared", krcName)
	}
	if cli.Binary == "" {
		return nil, fmt.Errorf("KRC %q: spec.cli.binary is empty", krcName)
	}
	if runner == nil {
		runner = DefaultCLIRunner{}
	}
	return &KubeClient{cli: cli, kubeContext: kubeContext, runner: runner, krcName: krcName}, nil
}

// Binary returns the KRC-declared binary name (for lookup/PATH checks).
func (k *KubeClient) Binary() string {
	if k == nil || k.cli == nil {
		return ""
	}
	return k.cli.Binary
}

// KubeContext returns the --to value the client was constructed with.
// Exposed so callers that need to forward the context string (SRC CLI
// invocations that still receive it as a plain template value) don't
// have to thread it separately.
func (k *KubeClient) KubeContext() string {
	if k == nil {
		return ""
	}
	return k.kubeContext
}

// KRCName returns the owning package name for diagnostics.
func (k *KubeClient) KRCName() string {
	if k == nil {
		return ""
	}
	return k.krcName
}

// intentArgs resolves the args template for a named intent and renders
// it against ctx. Missing intent: a structured error.
func (k *KubeClient) intentArgs(intent string, ctx CLIContext) ([]string, error) {
	if k == nil || k.cli == nil {
		return nil, fmt.Errorf("KubeClient is not initialised")
	}
	entry, ok := k.cli.Intents[intent]
	if !ok {
		return nil, fmt.Errorf("KRC %q: spec.cli.intents.%s is not declared; gitups needs it for the %q operation",
			k.krcName, intent, intent)
	}
	ctx.KubeContext = k.kubeContext
	return RenderIntentArgs(intent, entry.Args, ctx)
}

// run executes the binary with the given args and streams stdout/stderr
// to out. Returns the exit error from the CLI runner.
func (k *KubeClient) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return k.runner.Run(ctx, k.cli.Binary, args, stdout, stderr)
}

// Output executes binary + args and returns captured stdout. Used by
// intents that read cluster state (get-json, list-crds, server-version,
// list-pods-jsonpath, pod-logs).
func (k *KubeClient) output(ctx context.Context, args []string) ([]byte, []byte, error) {
	var stdout, stderr strbuf
	err := k.run(ctx, args, &stdout, &stderr)
	return stdout.Bytes(), stderr.Bytes(), err
}

// strbuf is a tiny bytes.Buffer wrapper that satisfies io.Writer.
type strbuf struct{ b []byte }

func (s *strbuf) Write(p []byte) (int, error) { s.b = append(s.b, p...); return len(p), nil }
func (s *strbuf) Bytes() []byte               { return s.b }

// Render returns the fully-rendered args slice for a named intent
// without executing it. Useful for logging the "would run" line before
// the call.
func (k *KubeClient) Render(intent string, ctx CLIContext) (string, []string, error) {
	args, err := k.intentArgs(intent, ctx)
	if err != nil {
		return "", nil, err
	}
	return k.cli.Binary, args, nil
}

// ApplyKustomize runs the apply (or apply-dry-run) intent against
// manifestPath. Echoes the rendered command to out.
func (k *KubeClient) ApplyKustomize(ctx context.Context, manifestPath string, dryRun bool, out io.Writer) error {
	intent := v1.IntentApply
	if dryRun {
		intent = v1.IntentApplyDryRun
	}
	args, err := k.intentArgs(intent, CLIContext{ManifestPath: manifestPath})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "gitups: %s %s\n", k.cli.Binary, strings.Join(args, " "))
	return k.run(ctx, args, out, out)
}

// GetJSON reads one resource as JSON. Returns stdout (the serialised
// resource body) on success. Errors include stderr when non-empty so
// NotFound / forbidden failures show up in apply logs.
func (k *KubeClient) GetJSON(ctx context.Context, namespace, kind, name string) ([]byte, error) {
	args, err := k.intentArgs(v1.IntentGetJSON, CLIContext{Namespace: namespace, Kind: kind, Name: name})
	if err != nil {
		return nil, err
	}
	stdout, stderr, err := k.output(ctx, args)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w (stderr: %s)", k.cli.Binary, strings.Join(args, " "), err, strings.TrimSpace(string(stderr)))
	}
	return stdout, nil
}

// WaitCondition blocks until a named Kind/Name in namespace satisfies
// the given status condition, up to timeout. The KRC decides how to
// implement the wait (kubectl wait --for=condition=<c>).
func (k *KubeClient) WaitCondition(ctx context.Context, namespace, kind, name, condition string, timeout time.Duration, out io.Writer) error {
	args, err := k.intentArgs(v1.IntentWaitCondition, CLIContext{
		Namespace: namespace, Kind: kind, Name: name, Condition: condition, Timeout: formatTimeout(timeout),
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "gitups: %s %s\n", k.cli.Binary, strings.Join(args, " "))
	return k.run(ctx, args, out, out)
}

// WaitCRDsEstablished blocks until every CRD has condition=Established.
// Timeout is passed to the KRC via {{.Timeout}}.
func (k *KubeClient) WaitCRDsEstablished(ctx context.Context, timeout time.Duration, out io.Writer) error {
	args, err := k.intentArgs(v1.IntentWaitCRDsEstablished, CLIContext{Timeout: formatTimeout(timeout)})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "gitups: %s %s\n", k.cli.Binary, strings.Join(args, " "))
	return k.run(ctx, args, out, out)
}

// ListCRDs returns the stdout of the list-crds intent. Used only to
// decide whether to run the establishment wait at all on a fresh
// cluster.
func (k *KubeClient) ListCRDs(ctx context.Context) ([]byte, error) {
	args, err := k.intentArgs(v1.IntentListCRDs, CLIContext{})
	if err != nil {
		return nil, err
	}
	stdout, _, err := k.output(ctx, args)
	return stdout, err
}

// ServerVersion returns the stdout of the server-version intent. The
// KRC is expected to return JSON; the caller parses ServerVersion.Major/Minor.
func (k *KubeClient) ServerVersion(ctx context.Context) ([]byte, error) {
	args, err := k.intentArgs(v1.IntentServerVersion, CLIContext{})
	if err != nil {
		return nil, err
	}
	stdout, _, err := k.output(ctx, args)
	return stdout, err
}

// ListPodsJSONPath runs the list-pods-jsonpath intent with the given
// selector. Used by CSV diagnostics to enumerate pods owned by a CSV.
func (k *KubeClient) ListPodsJSONPath(ctx context.Context, namespace, selector string) ([]byte, error) {
	args, err := k.intentArgs(v1.IntentListPodsJSONPath, CLIContext{Namespace: namespace, Selector: selector})
	if err != nil {
		return nil, err
	}
	stdout, _, err := k.output(ctx, args)
	return stdout, err
}

// PodLogs fetches logs from one pod via the pod-logs intent.
func (k *KubeClient) PodLogs(ctx context.Context, namespace, pod string) ([]byte, error) {
	args, err := k.intentArgs(v1.IntentPodLogs, CLIContext{Namespace: namespace, Pod: pod})
	if err != nil {
		return nil, err
	}
	stdout, _, err := k.output(ctx, args)
	return stdout, err
}

// formatTimeout renders a Go duration as a kubectl-style "Ns" token
// (dropping sub-second precision when the duration is a whole second).
// Intents that don't need timeouts can ignore the value; ones that do
// can pass it straight into kubectl's `--timeout=` flag.
func formatTimeout(d time.Duration) string {
	if d <= 0 {
		return "10m"
	}
	if d%time.Second == 0 {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	return d.String()
}

// ParseServerMinor extracts "<major>.<minor>" (e.g. "1.35") from a
// `kubectl version -o json` payload. Returns "" on any error. Centralised
// here so the compat-probe path has no kubectl-specific knowledge.
func ParseServerMinor(body []byte) string {
	var d struct {
		ServerVersion struct {
			Major string `json:"major"`
			Minor string `json:"minor"`
		} `json:"serverVersion"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return ""
	}
	if d.ServerVersion.Major == "" || d.ServerVersion.Minor == "" {
		return ""
	}
	return d.ServerVersion.Major + "." + strings.TrimRight(d.ServerVersion.Minor, "+")
}
