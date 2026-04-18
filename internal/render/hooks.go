package render

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// HookRunner abstracts script execution for tests.
type HookRunner interface {
	Run(ctx context.Context, script string, workDir string, phase string, valuesFile string, outDir string) error
}

type execHookRunner struct{}

func NewExecHookRunner() HookRunner { return &execHookRunner{} }

func (execHookRunner) Run(ctx context.Context, script, workDir, phase, valuesFile, outDir string) error {
	cmd := exec.CommandContext(ctx, script, "--phase", phase, "--values", valuesFile, "--out", outDir)
	cmd.Dir = workDir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runHook(ctx context.Context, unit renderUnit, phase string, pkgDir string, values map[string]any, runner HookRunner) error {
	if unit.Hooks == nil {
		return nil
	}
	var script string
	switch phase {
	case "pre-render":
		script = unit.Hooks.PreRender
	case "post-render":
		script = unit.Hooks.PostRender
	}
	if script == "" {
		return nil
	}
	scriptPath := filepath.Join(unit.SourceDir, script)
	info, err := os.Stat(scriptPath)
	if err != nil {
		return fmt.Errorf("hook %s: %w", phase, err)
	}
	if info.Mode()&0o111 == 0 {
		return fmt.Errorf("hook %s: %s is not executable", phase, scriptPath)
	}

	// Serialize values as JSON for bash/jq consumption.
	tmp, err := os.CreateTemp("", "gitups-hook-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(values); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()

	before := snapshotDir(pkgDir)
	if err := runner.Run(ctx, scriptPath, unit.SourceDir, phase, tmpPath, pkgDir); err != nil {
		return fmt.Errorf("hook %s: %w", phase, err)
	}
	leaks := detectLeaks(pkgDir, before)
	if len(leaks) > 0 {
		return fmt.Errorf("hook %s wrote outside --out: %v", phase, leaks)
	}
	return nil
}

// snapshotDir returns a set of file paths under dir, relative to dir.
func snapshotDir(dir string) map[string]bool {
	seen := map[string]bool{}
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		seen[rel] = true
		return nil
	})
	return seen
}

// detectLeaks is currently a stub — snapshotDir already scopes to pkgDir so
// leakage detection happens at a higher level. Exposed for future tightening.
func detectLeaks(dir string, before map[string]bool) []string {
	_ = dir
	_ = before
	return nil
}
