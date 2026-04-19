// Command gitups drives the workspace-oriented GitOps-bootstrap flow:
//
//	gitups init     <name> [-d <dir>]                           scaffold Provision
//	gitups expand   <name> [-d <dir>] [--force]                 Provision  -> FullProvision
//	gitups check    <name> [-d <dir>]                           validate Provision and FullProvision
//	gitups generate <name> [-d <dir>] [--context c] [--allow-placeholders]
//	                                                            FullProvision -> repo tree
//	gitups apply    <name> --to <ctx> [-d <dir>] [--dry-run] [--allow-placeholders] [--generate-secrets]
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
	"github.com/crmarques/gitups/internal/placeholders"
	"github.com/crmarques/gitups/internal/push"
	"github.com/crmarques/gitups/internal/render"
	"github.com/crmarques/gitups/internal/resolve"
	"github.com/crmarques/gitups/internal/secrets"
)

const defaultOutputDir = "./gitups-output-dir"

func main() {
	root := &cobra.Command{
		Use:           "gitups",
		Short:         "Scaffold, expand, check, and generate GitOps bootstrap repos",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newInitCmd(), newExpandCmd(), newCheckCmd(), newGenerateCmd(), newPushCmd(), newApplyCmd(), newWaitCmd(), newStatusCmd(), newPlanCmd(), newFillCmd())
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
		skipDetCheck      bool
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
			opts := render.Options{
				OutputPath:            ws.Root,
				KubectlContext:        ctx,
				AllowPlaceholders:     allowPlaceholders,
				SuppressFullProvision: true,
				PreserveExtras:        true,
				Prune:                 prune,
			}
			if err := render.Render(cmd.Context(), fp, cat, opts); err != nil {
				return err
			}
			if skipDetCheck {
				return nil
			}
			return verifyDeterminism(cmd.Context(), fp, cat, opts, ws, cmd.ErrOrStderr())
		},
	}
	addOutputDirFlag(cmd, &outputDir)
	cmd.Flags().StringVar(&kubectlContext, "context", "", "kubectl context label; defaults to current context")
	cmd.Flags().BoolVar(&allowPlaceholders, "allow-placeholders", false, "generate even when placeholders remain")
	cmd.Flags().BoolVar(&prune, "prune", false, "remove top-level directories not produced by this render pass")
	cmd.Flags().BoolVar(&skipDetCheck, "skip-determinism-check", false, "skip the second render pass that verifies byte-identical output")
	cmd.SetContext(context.Background())
	return cmd
}

// verifyDeterminism re-renders the same FullProvision into a scratch
// dir and compares against the workspace. Catches chart-side
// non-determinism (auto-generated TLS certs, random IDs, timestamps)
// inline at `generate` time instead of deferring to `status`. Writes
// a compact drift summary and returns an error so CI fails.
func verifyDeterminism(ctx context.Context, fp *v1.FullProvision, cat *catalog.Catalog, first render.Options, ws workspace, out writer) error {
	scratchRoot, err := os.MkdirTemp("", "gitups-det-")
	if err != nil {
		return fmt.Errorf("create determinism scratch: %w", err)
	}
	defer os.RemoveAll(scratchRoot)
	scratchOut := filepath.Join(scratchRoot, ws.Name)
	second := first
	second.OutputPath = scratchOut
	second.PreserveExtras = false
	second.Prune = false
	if err := render.Render(ctx, fp, cat, second); err != nil {
		return fmt.Errorf("determinism re-render: %w", err)
	}
	drifts, err := diffWorkspace(ws.Root, scratchOut)
	if err != nil {
		return fmt.Errorf("determinism diff: %w", err)
	}
	// Extras / orphan-dirs that come from PreserveExtras on the first
	// run but not the second are expected — filter them.
	filtered := drifts[:0]
	for _, d := range drifts {
		if d.Kind == "extra" || d.Kind == "orphan-dir" {
			continue
		}
		filtered = append(filtered, d)
	}
	if len(filtered) == 0 {
		return nil
	}
	fmt.Fprintf(out, "gitups: determinism check failed — %d file(s) differ between two render passes:\n", len(filtered))
	for _, d := range filtered {
		fmt.Fprintf(out, "  %-12s %s\n", d.Kind, d.Path)
	}
	return fmt.Errorf("non-deterministic render; inspect chart values for timestamps, random IDs, or auto-generated secrets, or pass --skip-determinism-check to bypass")
}

func newPushCmd() *cobra.Command {
	var (
		outputDir     string
		provider      string
		baseURL       string
		ownerType     string
		token         string
		user          string
		branch        string
		commitMessage string
		visibility    string
		createMissing bool
		flatten       bool
		force         bool
		dryRun        bool
	)
	cmd := &cobra.Command{
		Use:   "push <name> --provider <p> --base-url <url>",
		Short: "Publish rendered repos to a git provider (github|gitlab|gitea)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := newWorkspace(outputDir, args[0])
			if err != nil {
				return err
			}
			if _, err := os.Stat(ws.FullProvision); err != nil {
				return fmt.Errorf("%s not found (run `gitups generate %s` first)", ws.FullProvision, ws.Name)
			}
			fp, err := load.FullProvision(ws.FullProvision)
			if err != nil {
				return err
			}
			if fp.Metadata.Name != ws.Name {
				return fmt.Errorf("%s has metadata.name %q but workspace is %q",
					ws.FullProvision, fp.Metadata.Name, ws.Name)
			}
			if _, err := exec.LookPath("git"); err != nil {
				return fmt.Errorf("required binary %q not found in PATH", "git")
			}

			parsed, err := push.ParseBaseURL(baseURL)
			if err != nil {
				return err
			}
			if commitMessage == "" {
				commitMessage = fmt.Sprintf("gitups: sync %s", ws.Name)
			}
			resolvedToken := resolvePushToken(token, provider)

			prov, err := push.NewProvider(provider, push.ProviderConfig{
				Base:      parsed,
				Token:     resolvedToken,
				OwnerType: ownerType,
			})
			if err != nil {
				return err
			}

			out := cmd.ErrOrStderr()
			repos := renderedRepoNames(fp)
			if len(repos) == 0 {
				return fmt.Errorf("no rendered repos referenced by %s", ws.FullProvision)
			}

			_, err = push.Push(cmd.Context(), push.Config{
				WorkspaceRoot: ws.Root,
				RepoNames:     repos,
				Provider:      prov,
				Git:           push.DefaultGitRunner{Stderr: out},
				Out:           out,
			}, push.Options{
				Provider:      provider,
				BaseURL:       baseURL,
				OwnerType:     ownerType,
				Token:         resolvedToken,
				User:          user,
				Branch:        branch,
				CommitMessage: commitMessage,
				Visibility:    visibility,
				CreateMissing: createMissing,
				Flatten:       flatten,
				Force:         force,
				DryRun:        dryRun,
			}, parsed)
			return err
		},
	}
	addOutputDirFlag(cmd, &outputDir)
	cmd.Flags().StringVar(&provider, "provider", "", "git provider: github|gitlab|gitea (required)")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "HTTPS base URL including owner/group, e.g. https://github.com/myorg (required)")
	cmd.Flags().StringVar(&ownerType, "owner-type", "org", "GitHub/Gitea create-endpoint selector: org or user")
	cmd.Flags().StringVar(&token, "token", "", "override credentials for REST and push; else uses GITUPS_PUSH_TOKEN or provider env")
	cmd.Flags().StringVar(&user, "user", "", "basic-auth username injected into push URL when --token is set (default: provider-appropriate literal)")
	cmd.Flags().StringVar(&branch, "branch", "main", "branch to commit and push")
	cmd.Flags().StringVar(&commitMessage, "commit-message", "", "commit message when there are changes (default: \"gitups: sync <name>\")")
	cmd.Flags().StringVar(&visibility, "visibility", "private", "repo visibility on creation: private|public|internal")
	cmd.Flags().BoolVar(&createMissing, "create-missing", true, "create remote repo via provider API when absent")
	cmd.Flags().BoolVar(&flatten, "flatten", false, "replace '/' with '-' in rendered repo names for providers without subgroups")
	cmd.Flags().BoolVar(&force, "force", false, "force-push")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "do not create remote repos; pass --dry-run to git push")
	cmd.SetContext(context.Background())
	return cmd
}

// resolvePushToken applies the documented precedence: --token flag,
// GITUPS_PUSH_TOKEN, then provider-specific env vars. Keeping the
// fallback lookup in main (not in internal/push) means the core push
// package stays free of environment coupling and easier to unit-test.
func resolvePushToken(flag, provider string) string {
	if flag != "" {
		return flag
	}
	if v := os.Getenv("GITUPS_PUSH_TOKEN"); v != "" {
		return v
	}
	switch strings.ToLower(provider) {
	case "github":
		return os.Getenv("GITHUB_TOKEN")
	case "gitlab":
		return os.Getenv("GITLAB_TOKEN")
	case "gitea":
		return os.Getenv("GITEA_TOKEN")
	}
	return ""
}

// renderedRepoNames returns the distinct set of rendered repo dirs
// referenced by fp, in first-appearance order. Matches the apply-time
// ordering so a `push` followed by `apply` walks the same list.
func renderedRepoNames(fp *v1.FullProvision) []string {
	seen := map[string]bool{}
	var out []string
	for i := range fp.Spec.Packages {
		r := fp.Spec.Packages[i].RenderedPaths.Repo
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

func newApplyCmd() *cobra.Command {
	var (
		outputDir         string
		toContext         string
		dryRun            bool
		allowPlaceholders bool
		generateSecrets   bool
		waitCRDs          bool
		full              bool
		waitTimeout       time.Duration
	)
	cmd := &cobra.Command{
		Use:   "apply <name> --to <kubectl-context>",
		Short: "Bootstrap the target cluster: kubectl + SRC CLI for the bootstrap subset, then hand off to the in-cluster KRC/SRC",
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
			if generateSecrets {
				if err := fillGeneratedSecrets(fp, ws.FullProvision, cmd.ErrOrStderr()); err != nil {
					return err
				}
			}
			if !allowPlaceholders && len(fp.Spec.Placeholders) > 0 {
				return fmt.Errorf("%d unfilled placeholder(s) in %s (re-run with --allow-placeholders to force)",
					len(fp.Spec.Placeholders), ws.FullProvision)
			}
			if _, err := exec.LookPath("kubectl"); err != nil {
				return fmt.Errorf("required binary %q not found in PATH", "kubectl")
			}

			out := cmd.ErrOrStderr()

			// Decide flow: full-tree (today's behaviour) or bootstrap-only
			// (hand off to the KRC/SRC once their own units are applied).
			// --full wins; otherwise presence of a Provision-level
			// controllers block triggers the bootstrap flow.
			var prov *v1.Provision
			provPath := filepath.Join(ws.Root, "provision.yaml")
			if _, err := os.Stat(provPath); err == nil {
				prov, err = load.Provision(provPath)
				if err != nil {
					return fmt.Errorf("load provision %s: %w", provPath, err)
				}
			}
			hasControllers := prov != nil && prov.Spec.Controllers != nil &&
				(prov.Spec.Controllers.KubernetesResources != nil || prov.Spec.Controllers.ServiceResources != nil)

			if full || !hasControllers {
				return applyFullTree(cmd, fp, ws, toContext, dryRun, waitCRDs, waitTimeout, out)
			}
			return applyBootstrapOnly(cmd, fp, prov, ws, toContext, dryRun, waitCRDs, waitTimeout, out)
		},
	}
	addOutputDirFlag(cmd, &outputDir)
	cmd.Flags().StringVar(&toContext, "to", "", "kubectl context to apply into (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "kubectl apply --dry-run=server; no cluster state changes")
	cmd.Flags().BoolVar(&allowPlaceholders, "allow-placeholders", false, "apply even when placeholders remain in FullProvision")
	cmd.Flags().BoolVar(&generateSecrets, "generate-secrets", false, "before applying, fill placeholders whose input declared a generator and rewrite FullProvision in place")
	cmd.Flags().BoolVar(&waitCRDs, "wait-crds", false, "after each repo, wait for its OLM subscriptions to Succeed before the next repo")
	cmd.Flags().BoolVar(&full, "full", false, "apply the whole rendered tree via kubectl even when spec.controllers declares a KRC/SRC (for SRC-less setups or disaster recovery)")
	cmd.Flags().DurationVar(&waitTimeout, "wait-timeout", 10*time.Minute, "per-repo wait budget when --wait-crds is set")
	cmd.SetContext(context.Background())
	return cmd
}

// fillGeneratedSecrets fills every Placeholder whose Generator is set,
// rewrites FullProvision in place using the same yaml.Marshal pattern
// expand uses, and prints one line per fill (path + kind, never the
// value). No-op when nothing has a Generator.
func fillGeneratedSecrets(fp *v1.FullProvision, fpPath string, out writer) error {
	results, err := secrets.Fill(fp)
	if err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Fprintf(out, "gitups: --generate-secrets: no generator-bearing placeholders to fill\n")
		return nil
	}
	body, err := yaml.Marshal(fp)
	if err != nil {
		return fmt.Errorf("marshal full-provision: %w", err)
	}
	if err := os.WriteFile(fpPath, body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", fpPath, err)
	}
	for _, r := range results {
		fmt.Fprintf(out, "gitups: generated %s for %s\n", r.Kind, r.Path)
	}
	fmt.Fprintf(out, "gitups: --generate-secrets: filled %d placeholder(s); rewrote %s\n", len(results), fpPath)
	return nil
}

// applyFullTree keeps the today's kubectl apply -k <repo> per repo
// behaviour. Used when no controllers are declared and when --full is
// passed. Dependency ordering is implicit in fp.Spec.Packages' topo
// sort, so first-appearance per repo gives a safe apply order.
func applyFullTree(cmd *cobra.Command, fp *v1.FullProvision, ws workspace, toContext string, dryRun, waitCRDs bool, waitTimeout time.Duration, out writer) error {
	seen := map[string]bool{}
	var repoOrder []string
	for i := range fp.Spec.Packages {
		repo := fp.Spec.Packages[i].RenderedPaths.Repo
		if !seen[repo] {
			seen[repo] = true
			repoOrder = append(repoOrder, repo)
		}
	}
	fmt.Fprintf(out, "gitups: applying %d repo(s) to context %q (dry-run=%v, mode=full)\n",
		len(repoOrder), toContext, dryRun)
	for _, repo := range repoOrder {
		repoDir := filepath.Join(ws.Root, repo)
		if _, err := os.Stat(repoDir); err != nil {
			return fmt.Errorf("%s not rendered (run `gitups generate %s` first): %w", repo, ws.Name, err)
		}
		if err := kubectlApplyKustomize(cmd, toContext, repoDir, dryRun, out); err != nil {
			return err
		}
		if waitCRDs && !dryRun {
			subs := cluster.SubscriptionsForRepo(fp.Spec.Packages, repo)
			if len(subs) > 0 {
				fmt.Fprintf(out, "gitups: waiting on %d subscription(s) from %s before next repo\n", len(subs), repo)
				if err := cluster.WaitForSubscriptions(cmd.Context(), toContext, subs, cluster.WaitOptions{Timeout: waitTimeout, Out: out}); err != nil {
					return fmt.Errorf("wait after %s: %w", repo, err)
				}
			}
		}
	}
	fmt.Fprintf(out, "gitups: apply complete\n")
	return nil
}

// applyBootstrapOnly applies only the bootstrap subset: every install,
// every unit whose package role is KRC or SRC (their own instance /
// config / repo-secret), and every unit with a Controller pointer
// (KRC-synthesised managed-repo registrations + SRC-rewired
// managed-script jobs). Everything else is left for the in-cluster KRC
// to reconcile after the Applications it owns land in the cluster.
//
// Routing is per-unit, not per-repo, because the bootstrap subset
// spans multiple repos and skips siblings within each one. kubectl
// apply -k operates on each unit directory; SRC-owned units go through
// the declared SRC CLI.
func applyBootstrapOnly(cmd *cobra.Command, fp *v1.FullProvision, prov *v1.Provision, ws workspace, toContext string, dryRun, waitCRDs bool, waitTimeout time.Duration, out writer) error {
	cat, err := buildProvisionCatalog(prov, ws)
	if err != nil {
		return err
	}

	planned := bootstrapSubset(fp)
	if len(planned) == 0 {
		return fmt.Errorf("bootstrap subset is empty; nothing to apply")
	}
	sort.SliceStable(planned, func(i, j int) bool {
		if planned[i].ApplyWave != planned[j].ApplyWave {
			return planned[i].ApplyWave < planned[j].ApplyWave
		}
		return planned[i].Instance < planned[j].Instance
	})

	srcCLI, srcBinary, err := srcCLIForPlan(cat, prov, planned)
	if err != nil {
		return err
	}
	if srcBinary != "" {
		if _, err := exec.LookPath(srcBinary); err != nil {
			return fmt.Errorf("required SRC binary %q not found in PATH (declared in %s spec.cli)", srcBinary, srcCLI.ownerName)
		}
	}

	fmt.Fprintf(out, "gitups: bootstrap-only mode; %d unit(s) to apply to context %q (dry-run=%v)\n",
		len(planned), toContext, dryRun)
	writeBootstrapPlan(out, fp, planned)
	if warnings := compatibilityWarnings(cmd.Context(), cat, planned, toContext); len(warnings) > 0 {
		for _, w := range warnings {
			fmt.Fprintf(out, "gitups: compatibility warning — %s\n", w)
		}
	}

	appliedRepos := map[string]bool{}
	runner := cluster.DefaultCLIRunner{}
	for _, rp := range planned {
		unitDir := filepath.Join(ws.Root, rp.RenderedPaths.Repo, rp.RenderedPaths.Dir)
		if _, err := os.Stat(unitDir); err != nil {
			return fmt.Errorf("unit %s not rendered at %s (run `gitups generate %s` first): %w", rp.Instance, unitDir, ws.Name, err)
		}
		if rp.Controller != nil && rp.Controller.Kind == v1.RoleSRC {
			if err := invokeSRCCli(cmd.Context(), runner, srcCLI.spec, unitDir, toContext, rp, out, dryRun); err != nil {
				return err
			}
		} else {
			if err := kubectlApplyKustomize(cmd, toContext, unitDir, dryRun, out); err != nil {
				return err
			}
		}
		appliedRepos[rp.RenderedPaths.Repo] = true
		if waitCRDs && !dryRun && rp.Renderer == "olm" {
			ns, _ := rp.ResolvedValues["namespace"].(string)
			if ns != "" {
				sub := []cluster.SubscriptionRef{{Namespace: ns, Name: rp.Instance}}
				fmt.Fprintf(out, "gitups: waiting on subscription %s/%s\n", ns, rp.Instance)
				if err := cluster.WaitForSubscriptions(cmd.Context(), toContext, sub, cluster.WaitOptions{Timeout: waitTimeout, Out: out}); err != nil {
					return fmt.Errorf("wait after %s: %w", rp.Instance, err)
				}
			}
		}
	}
	fmt.Fprintf(out, "gitups: bootstrap complete; handoff to in-cluster KRC/SRC.\n")
	return nil
}

// writer narrows cobra's io.Writer to the subset we need. Eases testing.
type writer interface{ Write([]byte) (int, error) }

// kubectlApplyKustomize applies one kustomize dir with retry after CRD
// establishment. Shape lifted from the pre-Phase-4 per-repo apply so
// apply semantics remain identical for each bootstrap unit.
func kubectlApplyKustomize(cmd *cobra.Command, toContext, dir string, dryRun bool, out writer) error {
	kargs := []string{"--context", toContext, "apply", "-k", dir, "--server-side", "--force-conflicts"}
	if dryRun {
		kargs = append(kargs, "--dry-run=server")
	}
	run := func(label string) error {
		fmt.Fprintf(out, "gitups: kubectl %s [%s]\n", strings.Join(kargs, " "), label)
		k := exec.CommandContext(cmd.Context(), "kubectl", kargs...)
		k.Stdout = cmd.OutOrStdout()
		k.Stderr = out
		return k.Run()
	}
	if err := run("pass 1"); err != nil {
		if dryRun {
			return fmt.Errorf("kubectl apply -k %s: %w", dir, err)
		}
		fmt.Fprintf(out, "gitups: pass 1 reported errors; waiting for CRD establishment before retry\n")
		if waitErr := waitForCRDsEstablished(cmd.Context(), toContext, out); waitErr != nil {
			fmt.Fprintf(out, "gitups: CRD establishment wait did not complete cleanly: %v\n", waitErr)
		}
		if err2 := run("pass 2"); err2 != nil {
			return fmt.Errorf("kubectl apply -k %s (both passes failed): %w", dir, err2)
		}
	}
	return nil
}

// invokeSRCCli renders ControllerCLI.Args from the fixed template
// context and runs the SRC binary against unitDir. Namespace falls back
// to the unit's resolved values; apply --dry-run passes the
// `--dry-run=server` convention through by short-circuiting (we do not
// assume every SRC CLI supports it).
func invokeSRCCli(ctx context.Context, runner cluster.CLIRunner, spec *v1.ControllerCLI, unitDir, toContext string, rp *v1.ResolvedPackage, out writer, dryRun bool) error {
	if dryRun {
		fmt.Fprintf(out, "gitups: [dry-run] %s apply %s (skipped: SRC CLI has no uniform --dry-run contract)\n", spec.Binary, unitDir)
		return nil
	}
	ns, _ := rp.ResolvedValues["namespace"].(string)
	ctxFields := cluster.CLIContext{
		KubeContext:  toContext,
		ManifestPath: unitDir,
		Namespace:    ns,
	}
	args, err := cluster.RenderCLIArgs(spec, ctxFields)
	if err != nil {
		return fmt.Errorf("unit %s: %w", rp.Instance, err)
	}
	fmt.Fprintf(out, "gitups: %s %s\n", spec.Binary, strings.Join(args, " "))
	if err := runner.Run(ctx, spec.Binary, args, out, out); err != nil {
		return fmt.Errorf("unit %s: %s %s: %w", rp.Instance, spec.Binary, strings.Join(args, " "), err)
	}
	return nil
}

// compatibilityWarnings probes the target cluster's Kubernetes server
// version and cross-checks it against each planned package's declared
// spec.compatibility.kubernetes list. Returns a slice of human-readable
// strings to print at apply start. Never blocks apply — the strictest
// guarantee we offer today is "visible at the top of the run". Full
// semver satisfaction is deferred; this check uses string-prefix
// matching on the server minor version, which is enough to catch the
// "OLM v0.14 breaks on K8s 1.35" class of issue we saw in the dsv run.
func compatibilityWarnings(ctx context.Context, cat *catalog.Catalog, planned []*v1.ResolvedPackage, toContext string) []string {
	serverVer := kubeServerMinor(ctx, toContext)
	if serverVer == "" {
		return nil
	}
	seenPkg := map[string]bool{}
	var out []string
	for _, rp := range planned {
		entry, ok := cat.Lookup(rp.Template)
		if !ok {
			continue
		}
		name := entry.Def.Metadata.Name
		if seenPkg[name] {
			continue
		}
		seenPkg[name] = true
		c := entry.Def.Spec.Compatibility
		if c == nil || len(c.Kubernetes) == 0 {
			continue
		}
		if !k8sVersionSatisfies(serverVer, c.Kubernetes) {
			out = append(out, fmt.Sprintf("package %q declares compatibility %v; cluster reports %s",
				name, c.Kubernetes, serverVer))
		}
	}
	return out
}

// kubeServerMinor returns a string like "1.35" for the target
// cluster. Empty on any error.
func kubeServerMinor(ctx context.Context, kctx string) string {
	body, err := exec.CommandContext(ctx, "kubectl", "--context", kctx, "version", "-o", "json").Output()
	if err != nil {
		return ""
	}
	var d struct {
		ServerVersion struct {
			Major string `json:"major"`
			Minor string `json:"minor"`
		} `json:"serverVersion"`
	}
	if err := yaml.Unmarshal(body, &d); err != nil {
		return ""
	}
	if d.ServerVersion.Major == "" || d.ServerVersion.Minor == "" {
		return ""
	}
	// Minor often carries a "+" suffix for vendor-patched clusters.
	minor := strings.TrimRight(d.ServerVersion.Minor, "+")
	return d.ServerVersion.Major + "." + minor
}

// k8sVersionSatisfies applies a minimal compatibility grammar: each
// constraint is ">=1.N", "<1.N", "<=1.N", ">1.N", "==1.N", or plain
// "1.N". All constraints must match. Grammars outside this set are
// conservatively treated as "matched" so we don't false-alarm users on
// unfamiliar syntax — the check is a hint, not a gate.
func k8sVersionSatisfies(server string, constraints []string) bool {
	sMaj, sMin := parseMajorMinor(server)
	if sMaj == 0 {
		return true
	}
	for _, c := range constraints {
		c = strings.TrimSpace(c)
		op, rest := splitOp(c)
		cMaj, cMin := parseMajorMinor(rest)
		if cMaj == 0 {
			continue
		}
		cmp := (sMaj*1000 + sMin) - (cMaj*1000 + cMin)
		ok := true
		switch op {
		case ">=":
			ok = cmp >= 0
		case ">":
			ok = cmp > 0
		case "<=":
			ok = cmp <= 0
		case "<":
			ok = cmp < 0
		case "==", "":
			ok = cmp == 0
		}
		if !ok {
			return false
		}
	}
	return true
}

func splitOp(c string) (op, rest string) {
	for _, o := range []string{">=", "<=", "==", ">", "<"} {
		if strings.HasPrefix(c, o) {
			return o, strings.TrimSpace(c[len(o):])
		}
	}
	return "", c
}

func parseMajorMinor(s string) (maj, min int) {
	parts := strings.SplitN(s, ".", 3)
	if len(parts) < 2 {
		return 0, 0
	}
	for i, p := range parts[:2] {
		n := 0
		for _, r := range p {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		if i == 0 {
			maj = n
		} else {
			min = n
		}
	}
	return maj, min
}

// writeBootstrapPlan emits a one-line summary of direct vs deferred
// units plus an indented list of the direct set in apply order, so the
// user can see up-front what gitups owns before handoff. Large plans
// (>30) are truncated; re-run with `gitups plan` for a full listing.
func writeBootstrapPlan(out writer, fp *v1.FullProvision, planned []*v1.ResolvedPackage) {
	total := len(fp.Spec.Packages)
	deferred := total - len(planned)
	fmt.Fprintf(out, "gitups: plan — %d direct, %d deferred to KRC (total %d); handoff after direct set succeeds\n",
		len(planned), deferred, total)
	const maxList = 30
	for i, rp := range planned {
		if i == maxList {
			fmt.Fprintf(out, "gitups:   ... (+%d more; run `gitups plan %s` for the full list)\n",
				len(planned)-maxList, fp.Metadata.Name)
			break
		}
		fmt.Fprintf(out, "gitups:   [wave %d] %s (%s) → %s\n",
			rp.ApplyWave, rp.Instance, planUnitTag(rp), rp.RenderedPaths.Repo)
	}
}

// planUnitTag renders a compact descriptor of what kind of unit this
// is for plan output: "install/<renderer>", "resource/<template>",
// "<role>/self", or "<controller>/<intent>".
func planUnitTag(rp *v1.ResolvedPackage) string {
	switch {
	case rp.Controller != nil:
		return fmt.Sprintf("%s/%s", rp.Controller.Instance, rp.Controller.Intent)
	case rp.UnitType == v1.UnitTypeInstall:
		return fmt.Sprintf("install/%s", rp.InstallMethod)
	case rp.UnitType == v1.UnitTypeResource:
		if rp.Role == v1.RoleKRC || rp.Role == v1.RoleSRC {
			return fmt.Sprintf("%s/self", rp.Role)
		}
		return fmt.Sprintf("resource/%s", rp.ResourceTemplate)
	}
	return rp.UnitType
}

// bootstrapSubset picks every ResolvedPackage gitups apply must own
// directly: installs, controller-owned (KRC/SRC-synthesised or rewired)
// units, and the KRC/SRC packages' own resources.
func bootstrapSubset(fp *v1.FullProvision) []*v1.ResolvedPackage {
	var out []*v1.ResolvedPackage
	for i := range fp.Spec.Packages {
		rp := &fp.Spec.Packages[i]
		switch {
		case rp.UnitType == v1.UnitTypeInstall:
			out = append(out, rp)
		case rp.Controller != nil:
			out = append(out, rp)
		case rp.Role == v1.RoleKRC || rp.Role == v1.RoleSRC:
			out = append(out, rp)
		}
	}
	return out
}

// srcCLIBundle pairs a ControllerCLI spec with its owning package name
// for diagnostic messages.
type srcCLIBundle struct {
	spec      *v1.ControllerCLI
	ownerName string
}

// srcCLIForPlan returns the SRC's CLI spec if the plan contains any
// SRC-owned unit. The spec comes from the SRC package's PackageDefinition.
func srcCLIForPlan(cat *catalog.Catalog, prov *v1.Provision, plan []*v1.ResolvedPackage) (srcCLIBundle, string, error) {
	hasSRCOwned := false
	for _, rp := range plan {
		if rp.Controller != nil && rp.Controller.Kind == v1.RoleSRC {
			hasSRCOwned = true
			break
		}
	}
	if !hasSRCOwned {
		return srcCLIBundle{}, "", nil
	}
	if prov.Spec.Controllers == nil || prov.Spec.Controllers.ServiceResources == nil {
		return srcCLIBundle{}, "", fmt.Errorf("SRC-owned units present but spec.controllers.serviceResources is missing")
	}
	a := prov.Spec.Controllers.ServiceResources
	for _, r := range prov.Spec.Repositories {
		if r.Type != v1.RepoTypeKubernetesResources || r.RepoRef != nil || r.Name != a.Repo {
			continue
		}
		for _, pr := range r.Packages {
			entry, ok := cat.Lookup(pr.Template)
			if !ok {
				continue
			}
			instance := pr.Instance
			if instance == "" {
				parts := strings.Split(pr.Template, "/")
				instance = parts[len(parts)-1]
			}
			if instance != a.Instance {
				continue
			}
			if entry.Def.Spec.CLI == nil || entry.Def.Spec.CLI.Binary == "" {
				return srcCLIBundle{}, "", fmt.Errorf("SRC package %q has no spec.cli declared", entry.Def.Metadata.Name)
			}
			return srcCLIBundle{spec: entry.Def.Spec.CLI, ownerName: entry.Def.Metadata.Name}, entry.Def.Spec.CLI.Binary, nil
		}
	}
	return srcCLIBundle{}, "", fmt.Errorf("SRC instance %q not found in repo %q", a.Instance, a.Repo)
}

// buildProvisionCatalog resolves the catalog for the given provision
// relative to the workspace root. Mirrors the same interpretation of
// relative source paths used by `gitups expand`.
func buildProvisionCatalog(prov *v1.Provision, ws workspace) (*catalog.Catalog, error) {
	return catalog.Build(prov.Spec.Sources, ws.Root)
}

func waitForCRDsEstablished(ctx context.Context, kubectlContext string, out interface{ Write([]byte) (int, error) }) error {
	// On a fresh cluster no CRDs exist yet; `kubectl wait --all` emits
	// "no matching resources found" which looks like a failure but is
	// benign. Probe first and skip cleanly in that case.
	if !crdsExist(ctx, kubectlContext) {
		fmt.Fprintf(out, "gitups: no CRDs yet on %s; skipping establishment wait\n", kubectlContext)
		return nil
	}
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

// crdsExist returns true when the cluster has at least one CRD. Used
// to skip the --all establishment wait on fresh clusters where the
// underlying kubectl command would otherwise emit a false-alarm error.
func crdsExist(ctx context.Context, kubectlContext string) bool {
	args := []string{"--context", kubectlContext, "get", "crd", "-o", "name"}
	out, err := exec.CommandContext(ctx, "kubectl", args...).Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
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
	var (
		outputDir string
		showDiff  bool
		diffLines int
	)
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
				if showDiff && d.Kind == "modified" {
					writeDriftDiff(out, filepath.Join(scratchOut, d.Path), filepath.Join(ws.Root, d.Path), diffLines)
				}
			}
			return fmt.Errorf("drift detected; re-run `gitups generate %s` to reconcile", ws.Name)
		},
	}
	addOutputDirFlag(cmd, &outputDir)
	cmd.Flags().BoolVar(&showDiff, "diff", false, "print a unified diff for each modified file")
	cmd.Flags().IntVar(&diffLines, "diff-lines", 20, "max lines of diff to print per modified file (use 0 for unlimited)")
	cmd.SetContext(context.Background())
	return cmd
}

// writeDriftDiff prints a small unified-diff block between rendered
// (want) and workspace (have) so `status --diff` shows the offending
// lines directly. Kept minimal — not a full diff library — because
// typical renders diverge on a handful of lines and a massive dump is
// noise. Larger diffs get truncated with a tail-count hint.
func writeDriftDiff(out writer, want, have string, maxLines int) {
	wantBody, werr := os.ReadFile(want)
	haveBody, herr := os.ReadFile(have)
	if werr != nil || herr != nil {
		return
	}
	wantLines := strings.Split(string(wantBody), "\n")
	haveLines := strings.Split(string(haveBody), "\n")
	var lines []string
	// Simplified diff: find the first differing line, emit up to
	// maxLines of "want" vs "have" blocks.
	n := len(wantLines)
	if len(haveLines) < n {
		n = len(haveLines)
	}
	start := 0
	for start < n && wantLines[start] == haveLines[start] {
		start++
	}
	// Emit at most maxLines of context.
	for i := start; i < len(wantLines); i++ {
		if i >= start+maxLines {
			lines = append(lines, fmt.Sprintf("      ... (+%d more lines in want)", len(wantLines)-i))
			break
		}
		lines = append(lines, fmt.Sprintf("    - %s", wantLines[i]))
	}
	for i := start; i < len(haveLines); i++ {
		if i >= start+maxLines {
			lines = append(lines, fmt.Sprintf("      ... (+%d more lines in have)", len(haveLines)-i))
			break
		}
		lines = append(lines, fmt.Sprintf("    + %s", haveLines[i]))
	}
	for _, l := range lines {
		fmt.Fprintln(out, l)
	}
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
  #   path: ../../../gitups-packages/packages

  # Repositories select package installs and environment resources.
  repositories: []
  # - name: platform
  #   type: kubernetes-resources
  #   packages:
  #     - template: local/olm
  #     - template: local/metallb
  #       installMethod: helm
  # - name: platform-{{.Env}}
  #   type: kubernetes-resources
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

// newPlanCmd prints the apply plan (bootstrap subset or full tree)
// without touching the cluster. Useful for reviewing what gitups will
// own, in what order, and what it will defer to the in-cluster KRC.
func newPlanCmd() *cobra.Command {
	var (
		outputDir string
		full      bool
	)
	cmd := &cobra.Command{
		Use:   "plan <name>",
		Short: "Print the ordered apply plan without touching the cluster",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			var prov *v1.Provision
			provPath := filepath.Join(ws.Root, "provision.yaml")
			if _, err := os.Stat(provPath); err == nil {
				prov, _ = load.Provision(provPath)
			}
			hasControllers := prov != nil && prov.Spec.Controllers != nil &&
				(prov.Spec.Controllers.KubernetesResources != nil || prov.Spec.Controllers.ServiceResources != nil)

			out := cmd.OutOrStdout()
			if full || !hasControllers {
				seen := map[string]bool{}
				var repos []string
				for i := range fp.Spec.Packages {
					r := fp.Spec.Packages[i].RenderedPaths.Repo
					if !seen[r] {
						seen[r] = true
						repos = append(repos, r)
					}
				}
				fmt.Fprintf(out, "gitups: mode=full; %d repo(s), %d unit(s)\n", len(repos), len(fp.Spec.Packages))
				for _, r := range repos {
					fmt.Fprintf(out, "  repo %s\n", r)
					for i := range fp.Spec.Packages {
						rp := &fp.Spec.Packages[i]
						if rp.RenderedPaths.Repo != r {
							continue
						}
						fmt.Fprintf(out, "    [wave %d] %s (%s)\n", rp.ApplyWave, rp.Instance, planUnitTag(rp))
					}
				}
				return nil
			}
			planned := bootstrapSubset(fp)
			sort.SliceStable(planned, func(i, j int) bool {
				if planned[i].ApplyWave != planned[j].ApplyWave {
					return planned[i].ApplyWave < planned[j].ApplyWave
				}
				return planned[i].Instance < planned[j].Instance
			})
			fmt.Fprintf(out, "gitups: mode=bootstrap; %d direct, %d deferred to KRC (total %d)\n",
				len(planned), len(fp.Spec.Packages)-len(planned), len(fp.Spec.Packages))
			for _, rp := range planned {
				fmt.Fprintf(out, "  [wave %d] %-48s (%s) → %s\n", rp.ApplyWave, rp.Instance, planUnitTag(rp), rp.RenderedPaths.Repo)
			}
			// Deferred set: everything not in planned, grouped by repo.
			inPlan := map[string]bool{}
			for _, rp := range planned {
				inPlan[rp.Instance] = true
			}
			deferred := 0
			for i := range fp.Spec.Packages {
				if !inPlan[fp.Spec.Packages[i].Instance] {
					deferred++
				}
			}
			if deferred > 0 {
				fmt.Fprintf(out, "gitups: deferred to KRC — %d unit(s) land after handoff\n", deferred)
			}
			return nil
		},
	}
	addOutputDirFlag(cmd, &outputDir)
	cmd.Flags().BoolVar(&full, "full", false, "show the full-tree plan even when spec.controllers declares a KRC/SRC")
	cmd.SetContext(context.Background())
	return cmd
}

// newFillCmd implements CLI-native placeholder filling. Accepts
// repeated --set <instance>.<dotted.path>=<value> pairs; rewrites
// full-provision.yaml in place with the supplied values dropped into
// each package's resolvedValues. Clears the placeholders list when all
// sentinels are resolved; leaves unfilled entries alone so `expand`
// can re-emit them on the next pass.
func newFillCmd() *cobra.Command {
	var (
		outputDir string
		sets      []string
	)
	cmd := &cobra.Command{
		Use:   "fill <name>",
		Short: "Fill placeholders in FullProvision <name> via --set args",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			byInstance := map[string]*v1.ResolvedPackage{}
			for i := range fp.Spec.Packages {
				byInstance[fp.Spec.Packages[i].Instance] = &fp.Spec.Packages[i]
			}
			out := cmd.ErrOrStderr()
			for _, s := range sets {
				inst, path, val, err := parseFillSet(s)
				if err != nil {
					return fmt.Errorf("--set %q: %w", s, err)
				}
				rp, ok := byInstance[inst]
				if !ok {
					return fmt.Errorf("--set %q: instance %q not found in %s", s, inst, ws.FullProvision)
				}
				if rp.ResolvedValues == nil {
					rp.ResolvedValues = map[string]any{}
				}
				if err := setDottedPath(rp.ResolvedValues, path, val); err != nil {
					return fmt.Errorf("--set %q: %w", s, err)
				}
				fmt.Fprintf(out, "gitups: set %s.%s\n", inst, path)
			}
			// Re-scan placeholders so the summary reflects user fills.
			var remaining []v1.Placeholder
			for i := range fp.Spec.Packages {
				rp := &fp.Spec.Packages[i]
				if placeholders.Contains(rp.ResolvedValues) {
					// Keep only the entries whose leaf is still a sentinel.
					for _, ph := range fp.Spec.Placeholders {
						if strings.HasPrefix(ph.Path, fmt.Sprintf("spec.packages[%s].", rp.Instance)) {
							remaining = append(remaining, ph)
						}
					}
				}
			}
			fp.Spec.Placeholders = remaining
			body, err := yaml.Marshal(fp)
			if err != nil {
				return fmt.Errorf("marshal full-provision: %w", err)
			}
			if err := os.WriteFile(ws.FullProvision, body, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", ws.FullProvision, err)
			}
			fmt.Fprintf(out, "gitups: wrote %s (%d placeholder(s) remaining)\n", ws.FullProvision, len(remaining))
			return nil
		},
	}
	addOutputDirFlag(cmd, &outputDir)
	cmd.Flags().StringArrayVar(&sets, "set", nil, "repeatable: <instance>.<dotted.path>=<value>")
	cmd.SetContext(context.Background())
	return cmd
}

// parseFillSet parses --set <instance>.<dotted.path>=<value>. Instance
// ends at the first '.'; the rest up to '=' is the dotted path; the
// rest is a string value. Integer / bool coercion is deliberately
// NOT done here — descriptors declare types, and misaligned types in
// resolvedValues would be silently wrong. Users who need a number can
// edit full-provision.yaml directly.
func parseFillSet(s string) (instance, path, value string, err error) {
	eq := strings.IndexByte(s, '=')
	if eq < 0 {
		return "", "", "", fmt.Errorf("missing '='")
	}
	left := s[:eq]
	value = s[eq+1:]
	dot := strings.IndexByte(left, '.')
	if dot < 0 {
		return "", "", "", fmt.Errorf("missing '.' between <instance> and <path>")
	}
	instance = left[:dot]
	path = left[dot+1:]
	if instance == "" || path == "" {
		return "", "", "", fmt.Errorf("instance and path are both required")
	}
	return instance, path, value, nil
}

// setDottedPath writes v at the given dotted path, creating intermediate
// maps as needed. Array indices are not supported (use edit-in-place
// for nested arrays — intentional scope limit).
func setDottedPath(m map[string]any, path string, v any) error {
	parts := strings.Split(path, ".")
	cur := m
	for i, p := range parts {
		if i == len(parts)-1 {
			cur[p] = v
			return nil
		}
		next, ok := cur[p]
		if !ok {
			child := map[string]any{}
			cur[p] = child
			cur = child
			continue
		}
		nm, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("path %q: segment %q is not a map", path, p)
		}
		cur = nm
	}
	return nil
}
