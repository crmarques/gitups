package push

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitRunner executes `git` in a working directory. Real implementations
// shell out; tests inject a recorder.
type GitRunner interface {
	Run(ctx context.Context, dir string, args ...string) (stdout string, err error)
}

// DefaultGitRunner shells to the `git` binary on PATH. Stdout is
// captured and returned so callers can branch on it (e.g. detect empty
// staging area before committing). Stderr is streamed to the stderr
// writer stashed on the runner so progress lines reach the user.
type DefaultGitRunner struct {
	Stderr io.Writer
}

// Run implements GitRunner by invoking `git -C dir args...`.
func (r DefaultGitRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	c := exec.CommandContext(ctx, "git", full...)
	var out bytes.Buffer
	c.Stdout = &out
	if r.Stderr != nil {
		c.Stderr = r.Stderr
	} else {
		c.Stderr = os.Stderr
	}
	if err := c.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %w", strings.Join(full, " "), err)
	}
	return out.String(), nil
}

// EnsureRepo initializes dir as a git repo when .git is missing. Safe
// to call on dirs that are already git repos — it is a no-op then.
func EnsureRepo(ctx context.Context, git GitRunner, dir, branch string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return nil
	}
	if _, err := git.Run(ctx, dir, "init", "--initial-branch="+branch); err != nil {
		return fmt.Errorf("git init %s: %w", dir, err)
	}
	return nil
}

// SetRemote force-sets `origin` to url. Creates it if missing, updates
// it if it exists. Using set-url+add fallback keeps the call idempotent.
func SetRemote(ctx context.Context, git GitRunner, dir, url string) error {
	if _, err := git.Run(ctx, dir, "remote", "set-url", "origin", url); err != nil {
		if _, addErr := git.Run(ctx, dir, "remote", "add", "origin", url); addErr != nil {
			return fmt.Errorf("set remote: %w / %w", err, addErr)
		}
	}
	return nil
}

// StageAndCommit runs `git add -A` then commits if the index has
// changes. Returns (committed, error). Skipping the empty-commit case
// keeps push idempotent: re-running on an unchanged tree is a no-op
// plus a push that fast-forwards nothing.
func StageAndCommit(ctx context.Context, git GitRunner, dir, message string) (bool, error) {
	if _, err := git.Run(ctx, dir, "add", "-A"); err != nil {
		return false, err
	}
	// `diff --cached --quiet` exits 0 when the index matches HEAD
	// (nothing to commit) and 1 when it does not. Using `status
	// --porcelain` avoids HEAD-existence pitfalls on a fresh init.
	out, err := git.Run(ctx, dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(out) == "" {
		return false, nil
	}
	if _, err := git.Run(ctx, dir, "commit", "-m", message); err != nil {
		return false, fmt.Errorf("git commit: %w", err)
	}
	return true, nil
}

// GitPush runs `git push origin <branch>` with optional --force/--dry-run.
// Uses -u to set upstream on first push so subsequent `git status`
// inside the rendered dir shows the tracking branch.
func GitPush(ctx context.Context, git GitRunner, dir, branch string, force, dryRun bool) error {
	args := []string{"push", "-u", "origin", branch}
	if force {
		args = append(args, "--force")
	}
	if dryRun {
		args = append(args, "--dry-run")
	}
	if _, err := git.Run(ctx, dir, args...); err != nil {
		return err
	}
	return nil
}
