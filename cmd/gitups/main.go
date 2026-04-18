// Command gitups drives the workspace-oriented GitOps-bootstrap flow:
//
//	gitups init     <name> [-d <dir>]                           scaffold Provision
//	gitups expand   <name> [-d <dir>] [--force]                 Provision  -> FullProvision
//	gitups check    <name> [-d <dir>]                           validate Provision and FullProvision
//	gitups generate <name> [-d <dir>] [--context c] [--allow-placeholders]
//	                                                            FullProvision -> repo tree
//	gitups apply    <name> --to <ctx> [-d <dir>] [--dry-run] [--allow-placeholders]
//	                                                            kubectl apply -k each repo dir
//	gitups status   <name> [-d <dir>]                           drift report
//
// Each <name> owns a workspace at <dir>/<name>/ holding provision.yaml,
// full-provision.yaml, and the rendered repo subdirs as siblings.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
	"github.com/crmarques/gitups/internal/catalog"
	"github.com/crmarques/gitups/internal/cluster"
	"github.com/crmarques/gitups/internal/load"
	"github.com/crmarques/gitups/internal/render"
	"github.com/crmarques/gitups/internal/resolve"
)

const defaultOutputDir = "./gitups-output-dir"

func main() {
	root := &cobra.Command{
		Use:           "gitups",
		Short:         "Scaffold, expand, check, and generate GitOps bootstrap repos",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newInitCmd(), newExpandCmd(), newCheckCmd(), newGenerateCmd(), newApplyCmd(), newWaitCmd(), newStatusCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// workspace holds the resolved filesystem paths for a named environment.
type workspace struct {
	Name          string
	Root          string // <output-dir>/<name>
	Provision     string // <root>/provision.yaml
	FullProvision string // <root>/full-provision.yaml
}

func newWorkspace(outputDir, name string) (workspace, error) {
	if name == "" {
		return workspace{}, fmt.Errorf("name is required")
	}
	if strings.ContainsAny(name, `/\`) {
		return workspace{}, fmt.Errorf("name %q must not contain path separators", name)
	}
	if outputDir == "" {
		outputDir = defaultOutputDir
	}
	root := filepath.Join(outputDir, name)
	return workspace{
		Name:          name,
		Root:          root,
		Provision:     filepath.Join(root, "provision.yaml"),
		FullProvision: filepath.Join(root, "full-provision.yaml"),
	}, nil
}

func addOutputDirFlag(cmd *cobra.Command, target *string) {
	cmd.Flags().StringVarP(target, "output-dir", "d", defaultOutputDir,
		"workspace root; the environment lives at <output-dir>/<name>/")
}

func newInitCmd() *cobra.Command {
	var (
		outputDir string
		force     bool
	)
	cmd := &cobra.Command{
		Use:   "init <name>",
		Short: "Scaffold an empty Provision for <name>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := newWorkspace(outputDir, args[0])
			if err != nil {
				return err
			}
			if _, err := os.Stat(ws.Provision); err == nil && !force {
				return fmt.Errorf("%s already exists (pass --force to overwrite)", ws.Provision)
			}
			if err := os.MkdirAll(ws.Root, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", ws.Root, err)
			}
			if err := os.WriteFile(ws.Provision, []byte(scaffoldProvision(ws.Name)), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", ws.Provision, err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(),
				"gitups: scaffolded %s\n  next: edit spec.sources and spec.repositories, then `gitups expand %s`\n",
				ws.Provision, ws.Name)
			return nil
		},
	}
	addOutputDirFlag(cmd, &outputDir)
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing provision.yaml")
	return cmd
}

func newExpandCmd() *cobra.Command {
	var (
		outputDir string
		force     bool
	)
	cmd := &cobra.Command{
		Use:   "expand <name>",
		Short: "Expand Provision <name> into a FullProvision",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := newWorkspace(outputDir, args[0])
			if err != nil {
				return err
			}
			if _, err := os.Stat(ws.Provision); err != nil {
				return fmt.Errorf("%s not found (run `gitups init %s` first)", ws.Provision, ws.Name)
			}
			prov, extFrom, err := load.ProvisionResolved(ws.Provision)
			if err != nil {
				return err
			}
			if prov.Metadata.Name != ws.Name {
				return fmt.Errorf("%s has metadata.name %q but workspace is %q",
					ws.Provision, prov.Metadata.Name, ws.Name)
			}
			if load.IsScaffold(prov) {
				return fmt.Errorf("%s is still the init scaffold; fill in spec.sources and spec.repositories before `gitups expand %s`",
					ws.Provision, ws.Name)
			}
			if len(prov.Spec.Sources) == 0 {
				return fmt.Errorf("%s: spec.sources is empty", ws.Provision)
			}
			if len(prov.Spec.Repositories) == 0 {
				return fmt.Errorf("%s: spec.repositories is empty", ws.Provision)
			}
			baseDir := filepath.Dir(absPath(ws.Provision))
			cat, err := catalog.Build(prov.Spec.Sources, baseDir)
			if err != nil {
				return err
			}
			var prior *v1.FullProvision
			if !force {
				if _, statErr := os.Stat(ws.FullProvision); statErr == nil {
					prior, err = load.FullProvision(ws.FullProvision)
					if err != nil {
						return fmt.Errorf("load prior full-provision for idempotent expand: %w", err)
					}
				}
			}
			fp, err := resolve.Expand(prov, cat, resolve.Options{
				Prior:        prior,
				Force:        force,
				ExtendedFrom: extFrom,
			})
			if err != nil {
				return err
			}
			body, err := yaml.Marshal(fp)
			if err != nil {
				return err
			}
			if err := os.WriteFile(ws.FullProvision, body, 0o644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "gitups: wrote %s\n", ws.FullProvision)
			printPlaceholderSummary(cmd.ErrOrStderr(), fp)
			return nil
		},
	}
	addOutputDirFlag(cmd, &outputDir)
	cmd.Flags().BoolVar(&force, "force", false, "discard existing FullProvision and regenerate from scratch")
	return cmd
}

func newCheckCmd() *cobra.Command {
	var outputDir string
	cmd := &cobra.Command{
		Use:   "check <name>",
		Short: "Validate Provision and FullProvision for <name>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := newWorkspace(outputDir, args[0])
			if err != nil {
				return err
			}
			out := cmd.ErrOrStderr()
			if _, err := os.Stat(ws.Provision); err != nil {
				return fmt.Errorf("%s not found (run `gitups init %s`)", ws.Provision, ws.Name)
			}
			prov, extFrom, err := load.ProvisionResolved(ws.Provision)
			if err != nil {
				return fmt.Errorf("provision invalid: %w", err)
			}
			if prov.Metadata.Name != ws.Name {
				return fmt.Errorf("%s has metadata.name %q but workspace is %q",
					ws.Provision, prov.Metadata.Name, ws.Name)
			}
			if load.IsScaffold(prov) {
				fmt.Fprintf(out, "gitups: %s is the init scaffold (empty sources/repositories) — fill it in, then re-run check\n", ws.Provision)
				return nil
			}
			if extFrom != nil {
				fmt.Fprintf(out, "gitups: %s extends %s\n", ws.Provision, extFrom.Source)
			}
			fmt.Fprintf(out, "gitups: %s ok (%d source(s), %d repositories)\n",
				ws.Provision, len(prov.Spec.Sources), len(prov.Spec.Repositories))

			// Dry-expand the provision so catalog/resolve errors surface at
			// `check` time rather than first appearing in `expand`. We pass
			// nil prior so this is a pure validity probe, never touching the
			// on-disk FullProvision.
			baseDir := filepath.Dir(absPath(ws.Provision))
			cat, err := catalog.Build(prov.Spec.Sources, baseDir)
			if err != nil {
				return fmt.Errorf("catalog: %w", err)
			}
			if _, err := resolve.Expand(prov, cat, resolve.Options{}); err != nil {
				return fmt.Errorf("dry expand: %w", err)
			}
			fmt.Fprintf(out, "gitups: dry expand ok\n")

			if _, err := os.Stat(ws.FullProvision); err != nil {
				fmt.Fprintf(out, "gitups: %s not present — run `gitups expand %s`\n",
					ws.FullProvision, ws.Name)
				return nil
			}
			fp, err := load.FullProvision(ws.FullProvision)
			if err != nil {
				return fmt.Errorf("full-provision invalid: %w", err)
			}
			if fp.Metadata.Name != ws.Name {
				return fmt.Errorf("%s has metadata.name %q but workspace is %q",
					ws.FullProvision, fp.Metadata.Name, ws.Name)
			}
			fmt.Fprintf(out, "gitups: %s ok (%d package(s))\n",
				ws.FullProvision, len(fp.Spec.Packages))
			printPlaceholderSummary(out, fp)
			return nil
		},
	}
	addOutputDirFlag(cmd, &outputDir)
	return cmd
}

func newGenerateCmd() *cobra.Command {
	var (
		outputDir         string
		kubectlContext    string
		allowPlaceholders bool
		prune             bool
	)
	cmd := &cobra.Command{
		Use:   "generate <name>",
		Short: "Generate repo directories from FullProvision <name>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := newWorkspace(outputDir, args[0])
			if err != nil {
				return err
			}
			if _, err := os.Stat(ws.FullProvision); err != nil {
				return fmt.Errorf("%s not found (run `gitups expand %s` first)",
					ws.FullProvision, ws.Name)
			}
			fp, err := load.FullProvision(ws.FullProvision)
			if err != nil {
				return err
			}
			if fp.Metadata.Name != ws.Name {
				return fmt.Errorf("%s has metadata.name %q but workspace is %q",
					ws.FullProvision, fp.Metadata.Name, ws.Name)
			}
			baseDir := filepath.Dir(absPath(ws.FullProvision))
			cat, err := catalog.Build(fp.Spec.Sources, baseDir)
			if err != nil {
				return err
			}
			if err := ensureBinaries(); err != nil {
				return err
			}
			ctx := kubectlContext
			if ctx == "" {
				ctx = currentKubectlContext()
			}
			return render.Render(cmd.Context(), fp, cat, render.Options{
				OutputPath:            ws.Root,
				KubectlContext:        ctx,
				AllowPlaceholders:     allowPlaceholders,
				SuppressFullProvision: true,
				PreserveExtras:        true,
				Prune:                 prune,
			})
		},
	}
	addOutputDirFlag(cmd, &outputDir)
	cmd.Flags().StringVar(&kubectlContext, "context", "", "kubectl context label; defaults to current context")
	cmd.Flags().BoolVar(&allowPlaceholders, "allow-placeholders", false, "generate even when placeholders remain")
	cmd.Flags().BoolVar(&prune, "prune", false, "remove top-level directories not produced by this render pass")
	cmd.SetContext(context.Background())
	return cmd
}

func newApplyCmd() *cobra.Command {
	var (
		outputDir         string
		toContext         string
		dryRun            bool
		allowPlaceholders bool
		waitCRDs          bool
		waitTimeout       time.Duration
	)
	cmd := &cobra.Command{
		Use:   "apply <name> --to <kubectl-context>",
		Short: "kubectl apply -k each rendered repo dir in dependency order",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if toContext == "" {
				return fmt.Errorf("--to <kubectl-context> is required")
			}
			ws, err := newWorkspace(outputDir, args[0])
			if err != nil {
				return err
			}
			if _, err := os.Stat(ws.FullProvision); err != nil {
				return fmt.Errorf("%s not found (run `gitups expand %s` and `gitups generate %s` first)",
					ws.FullProvision, ws.Name, ws.Name)
			}
			fp, err := load.FullProvision(ws.FullProvision)
			if err != nil {
				return err
			}
			if fp.Metadata.Name != ws.Name {
				return fmt.Errorf("%s has metadata.name %q but workspace is %q",
					ws.FullProvision, fp.Metadata.Name, ws.Name)
			}
			if !allowPlaceholders && len(fp.Spec.Placeholders) > 0 {
				return fmt.Errorf("%d unfilled placeholder(s) in %s (re-run with --allow-placeholders to force)",
					len(fp.Spec.Placeholders), ws.FullProvision)
			}
			if _, err := exec.LookPath("kubectl"); err != nil {
				return fmt.Errorf("required binary %q not found in PATH", "kubectl")
			}

			// Order repos by the first appearance of a resolved unit in each
			// repo. Empty repos never appear in fp.Spec.Packages, so they
			// cannot be applied.
			seen := map[string]bool{}
			var repoOrder []string
			for i := range fp.Spec.Packages {
				repo := fp.Spec.Packages[i].RenderedPaths.Repo
				if !seen[repo] {
					seen[repo] = true
					repoOrder = append(repoOrder, repo)
				}
			}

			out := cmd.ErrOrStderr()
			fmt.Fprintf(out, "gitups: applying %d repo(s) to context %q (dry-run=%v)\n",
				len(repoOrder), toContext, dryRun)
			for _, repo := range repoOrder {
				repoDir := filepath.Join(ws.Root, repo)
				if _, err := os.Stat(repoDir); err != nil {
					return fmt.Errorf("%s not rendered (run `gitups generate %s` first): %w", repo, ws.Name, err)
				}
				// Server-side apply sidesteps the 256KB last-applied annotation
				// limit that OLM's CRDs blow through, and force-conflicts lets
				// us re-take ownership of fields managed by a prior client-side
				// apply. When both a CRD and its CRs live in the same repo
				// (OLM bootstrap), the first pass establishes the CRDs and the
				// second pass lands the CRs; we retry once on failure.
				kargs := []string{"--context", toContext, "apply", "-k", repoDir, "--server-side", "--force-conflicts"}
				if dryRun {
					kargs = append(kargs, "--dry-run=server")
				}
				applyOnce := func(label string) error {
					fmt.Fprintf(out, "gitups: kubectl %s [%s]\n", strings.Join(kargs, " "), label)
					k := exec.CommandContext(cmd.Context(), "kubectl", kargs...)
					k.Stdout = cmd.OutOrStdout()
					k.Stderr = out
					return k.Run()
				}
				if err := applyOnce("pass 1"); err != nil {
					if dryRun {
						return fmt.Errorf("kubectl apply -k %s: %w", repoDir, err)
					}
					fmt.Fprintf(out, "gitups: pass 1 reported errors; waiting for CRD establishment before retry\n")
					if waitErr := waitForCRDsEstablished(cmd.Context(), toContext, out); waitErr != nil {
						fmt.Fprintf(out, "gitups: CRD establishment wait did not complete cleanly: %v\n", waitErr)
					}
					if err2 := applyOnce("pass 2"); err2 != nil {
						return fmt.Errorf("kubectl apply -k %s (both passes failed): %w", repoDir, err2)
					}
				}
				// Optional: wait for operator CSVs installed by this repo
				// to reach Succeeded before moving to the next repo. Lets
				// later repos safely reference CRDs the operator installs.
				if waitCRDs && !dryRun {
					subs := cluster.SubscriptionsForRepo(fp.Spec.Packages, repo)
					if len(subs) > 0 {
						fmt.Fprintf(out, "gitups: waiting on %d subscription(s) from %s before next repo\n",
							len(subs), repo)
						if err := cluster.WaitForSubscriptions(cmd.Context(), toContext, subs, cluster.WaitOptions{
							Timeout: waitTimeout,
							Out:     out,
						}); err != nil {
							return fmt.Errorf("wait after %s: %w", repo, err)
						}
					}
				}
			}
			fmt.Fprintf(out, "gitups: apply complete\n")
			return nil
		},
	}
	addOutputDirFlag(cmd, &outputDir)
	cmd.Flags().StringVar(&toContext, "to", "", "kubectl context to apply into (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "kubectl apply --dry-run=server; no cluster state changes")
	cmd.Flags().BoolVar(&allowPlaceholders, "allow-placeholders", false, "apply even when placeholders remain in FullProvision")
	cmd.Flags().BoolVar(&waitCRDs, "wait-crds", false, "after each repo, wait for its OLM subscriptions to Succeed before the next repo")
	cmd.Flags().DurationVar(&waitTimeout, "wait-timeout", 10*time.Minute, "per-repo wait budget when --wait-crds is set")
	cmd.SetContext(context.Background())
	return cmd
}

func waitForCRDsEstablished(ctx context.Context, kubectlContext string, out interface{ Write([]byte) (int, error) }) error {
	args := []string{
		"--context", kubectlContext,
		"wait",
		"--for=condition=Established",
		"crd",
		"--all",
		"--timeout=60s",
	}
	fmt.Fprintf(out, "gitups: kubectl %s\n", strings.Join(args, " "))
	k := exec.CommandContext(ctx, "kubectl", args...)
	k.Stdout = out
	k.Stderr = out
	return k.Run()
}

func newWaitCmd() *cobra.Command {
	var (
		outputDir string
		toContext string
		timeout   time.Duration
	)
	cmd := &cobra.Command{
		Use:   "wait <name> --to <kubectl-context>",
		Short: "Block until OLM subscriptions referenced by <name> have Succeeded CSVs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if toContext == "" {
				return fmt.Errorf("--to <kubectl-context> is required")
			}
			ws, err := newWorkspace(outputDir, args[0])
			if err != nil {
				return err
			}
			if _, err := os.Stat(ws.FullProvision); err != nil {
				return fmt.Errorf("%s not found (run `gitups expand %s` first)", ws.FullProvision, ws.Name)
			}
			fp, err := load.FullProvision(ws.FullProvision)
			if err != nil {
				return err
			}
			if fp.Metadata.Name != ws.Name {
				return fmt.Errorf("%s has metadata.name %q but workspace is %q",
					ws.FullProvision, fp.Metadata.Name, ws.Name)
			}
			if _, err := exec.LookPath("kubectl"); err != nil {
				return fmt.Errorf("required binary %q not found in PATH", "kubectl")
			}
			subs := cluster.SubscriptionsFromPackages(fp.Spec.Packages)
			out := cmd.ErrOrStderr()
			if len(subs) == 0 {
				fmt.Fprintf(out, "gitups: no OLM subscriptions in %s\n", ws.FullProvision)
				return nil
			}
			fmt.Fprintf(out, "gitups: waiting on %d subscription(s) in context %q (timeout %s)\n",
				len(subs), toContext, timeout)
			return cluster.WaitForSubscriptions(cmd.Context(), toContext, subs, cluster.WaitOptions{
				Timeout: timeout,
				Out:     out,
			})
		},
	}
	addOutputDirFlag(cmd, &outputDir)
	cmd.Flags().StringVar(&toContext, "to", "", "kubectl context to talk to (required)")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "overall wait budget across all subscriptions")
	cmd.SetContext(context.Background())
	return cmd
}

func newStatusCmd() *cobra.Command {
	var outputDir string
	cmd := &cobra.Command{
		Use:   "status <name>",
		Short: "Report drift between rendered repos and FullProvision <name>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := newWorkspace(outputDir, args[0])
			if err != nil {
				return err
			}
			if _, err := os.Stat(ws.FullProvision); err != nil {
				return fmt.Errorf("%s not found (run `gitups expand %s` first)",
					ws.FullProvision, ws.Name)
			}
			fp, err := load.FullProvision(ws.FullProvision)
			if err != nil {
				return err
			}
			if fp.Metadata.Name != ws.Name {
				return fmt.Errorf("%s has metadata.name %q but workspace is %q",
					ws.FullProvision, fp.Metadata.Name, ws.Name)
			}
			baseDir := filepath.Dir(absPath(ws.FullProvision))
			cat, err := catalog.Build(fp.Spec.Sources, baseDir)
			if err != nil {
				return err
			}
			if err := ensureBinaries(); err != nil {
				return err
			}
			// Render into a scratch sibling dir so status never mutates the
			// workspace. AllowPlaceholders so a half-filled FullProvision still
			// produces a diff — status is a read-only probe.
			scratchRoot, err := os.MkdirTemp("", "gitups-status-")
			if err != nil {
				return fmt.Errorf("create scratch: %w", err)
			}
			defer os.RemoveAll(scratchRoot)
			scratchOut := filepath.Join(scratchRoot, ws.Name)
			if err := render.Render(cmd.Context(), fp, cat, render.Options{
				OutputPath:            scratchOut,
				KubectlContext:        currentKubectlContext(),
				AllowPlaceholders:     true,
				SuppressFullProvision: true,
			}); err != nil {
				return fmt.Errorf("dry render: %w", err)
			}
			drifts, err := diffWorkspace(ws.Root, scratchOut)
			if err != nil {
				return err
			}
			out := cmd.ErrOrStderr()
			if len(drifts) == 0 {
				fmt.Fprintf(out, "gitups: %s is up to date with %s\n", ws.Root, ws.FullProvision)
				return nil
			}
			fmt.Fprintf(out, "gitups: %d drift(s) between %s and %s:\n",
				len(drifts), ws.Root, ws.FullProvision)
			for _, d := range drifts {
				fmt.Fprintf(out, "  %-12s %s\n", d.Kind, d.Path)
			}
			return fmt.Errorf("drift detected; re-run `gitups generate %s` to reconcile", ws.Name)
		},
	}
	addOutputDirFlag(cmd, &outputDir)
	cmd.SetContext(context.Background())
	return cmd
}

// drift is a single entry in a status report.
type drift struct {
	Kind string // missing | modified | extra | orphan-dir
	Path string // workspace-relative display path
}

// diffWorkspace compares a freshly-rendered tree against the workspace's
// rendered repo siblings. Top-level workspace files (provision.yaml,
// full-provision.yaml) are ignored by construction — only directory siblings
// are walked.
func diffWorkspace(wsRoot, rendered string) ([]drift, error) {
	rEntries, err := os.ReadDir(rendered)
	if err != nil {
		return nil, fmt.Errorf("read rendered: %w", err)
	}
	var drifts []drift
	rendereredRepos := map[string]bool{}
	for _, e := range rEntries {
		if !e.IsDir() {
			continue
		}
		rendereredRepos[e.Name()] = true
		wsPath := filepath.Join(wsRoot, e.Name())
		rPath := filepath.Join(rendered, e.Name())
		if _, err := os.Stat(wsPath); errors.Is(err, fs.ErrNotExist) {
			drifts = append(drifts, drift{Kind: "missing-dir", Path: e.Name() + "/"})
			// still enumerate files so the user sees what's missing
		}
		if err := compareRepoTree(rPath, wsPath, e.Name(), &drifts); err != nil {
			return nil, err
		}
	}
	wsEntries, err := os.ReadDir(wsRoot)
	if err == nil {
		for _, e := range wsEntries {
			if !e.IsDir() {
				continue
			}
			if !rendereredRepos[e.Name()] {
				drifts = append(drifts, drift{Kind: "orphan-dir", Path: e.Name() + "/"})
			}
		}
	}
	sort.SliceStable(drifts, func(i, j int) bool {
		if drifts[i].Path == drifts[j].Path {
			return drifts[i].Kind < drifts[j].Kind
		}
		return drifts[i].Path < drifts[j].Path
	})
	return drifts, nil
}

// compareRepoTree walks every file under rendered and reports missing/modified
// files in workspace, then walks workspace and reports files that rendered did
// not produce.
func compareRepoTree(rendered, workspace, prefix string, drifts *[]drift) error {
	rFiles := map[string]bool{}
	err := filepath.WalkDir(rendered, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(rendered, p)
		rFiles[rel] = true
		displayPath := filepath.Join(prefix, rel)
		wsFile := filepath.Join(workspace, rel)
		wsBody, werr := os.ReadFile(wsFile)
		if errors.Is(werr, fs.ErrNotExist) {
			*drifts = append(*drifts, drift{Kind: "missing", Path: displayPath})
			return nil
		}
		if werr != nil {
			return werr
		}
		rBody, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if !bytes.Equal(rBody, wsBody) {
			*drifts = append(*drifts, drift{Kind: "modified", Path: displayPath})
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Workspace-only files: walk workspace even if it doesn't exist (noop).
	if _, err := os.Stat(workspace); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(workspace, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(workspace, p)
		if !rFiles[rel] {
			*drifts = append(*drifts, drift{Kind: "extra", Path: filepath.Join(prefix, rel)})
		}
		return nil
	})
}

// scaffoldProvision produces a minimal Provision YAML carrying only metadata,
// with commented-out examples to guide the user's first edit.
func scaffoldProvision(name string) string {
	return fmt.Sprintf(`apiVersion: gitups/v1alpha1
kind: Provision
metadata:
  name: %s
spec:
  # Package sources: where gitups looks up package definitions.
  # v0.1 supports "filesystem" sources only. Path is relative to this file.
  sources: []
  # - name: local
  #   type: filesystem
  #   path: ../../../gitups-packages

  # Repositories select package installs and environment resources.
  repositories: []
  # - name: platform
  #   type: k8s-gitops-generic
  #   packages:
  #     - template: local/olm
  #     - template: local/metallb
  #       installMethod: helm
  # - name: platform-{{.Env}}
  #   type: k8s-gitops-env
  #   repoRef:
  #     name: platform
  #     commit: v0.0.1
`, name)
}

func printPlaceholderSummary(w interface{ Write([]byte) (int, error) }, fp *v1.FullProvision) {
	if len(fp.Spec.Placeholders) == 0 {
		fmt.Fprintf(stdioWriter{w}, "gitups: no placeholders; ready to generate.\n")
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "gitups: %d placeholder(s) require user input:\n", len(fp.Spec.Placeholders))
	for _, ph := range fp.Spec.Placeholders {
		tag := ""
		if ph.Sensitive {
			tag = " [sensitive]"
		}
		fmt.Fprintf(&b, "  %s%s — %s\n", ph.Path, tag, ph.Reason)
	}
	_, _ = w.Write([]byte(b.String()))
}

type stdioWriter struct {
	w interface{ Write([]byte) (int, error) }
}

func (s stdioWriter) Write(p []byte) (int, error) { return s.w.Write(p) }

func absPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

func ensureBinaries() error {
	for _, bin := range []string{"helm", "kustomize"} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("required binary %q not found in PATH", bin)
		}
	}
	return nil
}

func currentKubectlContext() string {
	out, err := exec.Command("kubectl", "config", "current-context").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
