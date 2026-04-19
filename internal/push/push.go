package push

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// Plan describes what Push intends to do for one rendered repo. The
// CLI uses it to print a structured summary; tests assert on it.
type Plan struct {
	RepoName   string
	RemoteName string
	LocalDir   string
	CloneURL   string
	Created    bool
}

// Config holds the non-option wiring needed by Push: the workspace
// layout, the provider (stubbable), the git runner (stubbable), and an
// output sink for progress lines.
type Config struct {
	WorkspaceRoot string
	RepoNames     []string // rendered repo dirs (top-level of WorkspaceRoot)
	Provider      Provider
	Git           GitRunner
	Out           io.Writer
}

// Push publishes each rendered repo dir to Config.Provider using the
// supplied Options. Order is stable (alphabetical by repo name) so
// output diffs cleanly across runs.
func Push(ctx context.Context, cfg Config, opts Options, parsed ParsedBaseURL) ([]Plan, error) {
	if cfg.Provider == nil {
		return nil, fmt.Errorf("push: provider is nil")
	}
	if cfg.Git == nil {
		return nil, fmt.Errorf("push: git runner is nil")
	}
	if cfg.Out == nil {
		cfg.Out = io.Discard
	}
	if len(cfg.RepoNames) == 0 {
		return nil, fmt.Errorf("push: no rendered repos found under %s (run `gitups generate` first)", cfg.WorkspaceRoot)
	}
	names := append([]string(nil), cfg.RepoNames...)
	sort.Strings(names)

	var plans []Plan
	for _, name := range names {
		repoDir := filepath.Join(cfg.WorkspaceRoot, name)
		if info, err := os.Stat(repoDir); err != nil || !info.IsDir() {
			return plans, fmt.Errorf("push: rendered repo %q not found at %s", name, repoDir)
		}
		remote := RemoteRepoName(name, opts.Flatten)

		fmt.Fprintf(cfg.Out, "gitups: push %s -> %s/%s/%s\n", name, parsed.Host, parsed.Owner, remote)

		cloneURL, err := cfg.Provider.EnsureRepo(ctx, parsed.Owner, remote, opts.Visibility, opts.CreateMissing && !opts.DryRun)
		if err != nil {
			return plans, err
		}
		pushURL, err := InjectCredentials(cloneURL, opts.User, opts.Token)
		if err != nil {
			return plans, err
		}
		if err := EnsureRepo(ctx, cfg.Git, repoDir, opts.Branch); err != nil {
			return plans, err
		}
		if err := SetRemote(ctx, cfg.Git, repoDir, pushURL); err != nil {
			return plans, err
		}
		committed, err := StageAndCommit(ctx, cfg.Git, repoDir, opts.CommitMessage)
		if err != nil {
			return plans, err
		}
		if committed {
			fmt.Fprintf(cfg.Out, "gitups: committed changes in %s\n", name)
		} else {
			fmt.Fprintf(cfg.Out, "gitups: no changes to commit in %s\n", name)
		}
		if err := GitPush(ctx, cfg.Git, repoDir, opts.Branch, opts.Force, opts.DryRun); err != nil {
			return plans, fmt.Errorf("git push %s: %w", name, err)
		}
		plans = append(plans, Plan{
			RepoName:   name,
			RemoteName: remote,
			LocalDir:   repoDir,
			CloneURL:   cloneURL,
		})
	}
	fmt.Fprintf(cfg.Out, "gitups: push complete (%d repo(s))\n", len(plans))
	return plans, nil
}
