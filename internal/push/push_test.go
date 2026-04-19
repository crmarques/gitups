package push

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGit records every invocation and returns canned responses.
// Matching on the first arg after `-C <dir>` is enough for the
// handful of commands push.go runs (`init`, `remote`, `add`, `status`,
// `commit`, `push`).
type fakeGit struct {
	calls      [][]string
	statusBody string // returned on `status --porcelain`
}

func (f *fakeGit) Run(_ context.Context, dir string, args ...string) (string, error) {
	full := append([]string{dir}, args...)
	f.calls = append(f.calls, full)
	if len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain" {
		return f.statusBody, nil
	}
	return "", nil
}

type fakeProvider struct {
	host, owner string
	calls       []string
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) EnsureRepo(_ context.Context, owner, repo, _ string, _ bool) (string, error) {
	f.calls = append(f.calls, owner+"/"+repo)
	return "https://" + f.host + "/" + owner + "/" + repo + ".git", nil
}

func TestPushHappyPath(t *testing.T) {
	ws := t.TempDir()
	for _, name := range []string{"basic-infra", "basic-infra-dev"} {
		repo := filepath.Join(ws, name)
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := &fakeGit{statusBody: " M README.md\n"}
	prov := &fakeProvider{host: "gitea.example.com", owner: "myorg"}
	var buf bytes.Buffer

	plans, err := Push(context.Background(), Config{
		WorkspaceRoot: ws,
		RepoNames:     []string{"basic-infra-dev", "basic-infra"},
		Provider:      prov,
		Git:           git,
		Out:           &buf,
	}, Options{
		Branch:        "main",
		CommitMessage: "gitups: sync",
		Token:         "tkn",
	}, ParsedBaseURL{Scheme: "https", Host: "gitea.example.com", Owner: "myorg"})
	if err != nil {
		t.Fatalf("Push: %v\n%s", err, buf.String())
	}
	if len(plans) != 2 {
		t.Fatalf("plans: want 2, got %d", len(plans))
	}
	// Alphabetical ordering is part of the contract.
	if plans[0].RepoName != "basic-infra" || plans[1].RepoName != "basic-infra-dev" {
		t.Errorf("order: %v", plans)
	}
	// Provider was consulted in alphabetical order.
	if got := prov.calls; len(got) != 2 || got[0] != "myorg/basic-infra" || got[1] != "myorg/basic-infra-dev" {
		t.Errorf("provider calls: %v", got)
	}
	// Each repo should have run init, set-url, add, status, commit, push.
	// The first arg of each call is the working dir; check key verbs.
	verbs := map[string]int{}
	for _, c := range git.calls {
		if len(c) < 2 {
			continue
		}
		verbs[c[1]]++
	}
	for _, want := range []string{"init", "remote", "add", "status", "commit", "push"} {
		if verbs[want] < 2 {
			t.Errorf("git %s ran %d times, want >= 2", want, verbs[want])
		}
	}
}

func TestPushSkipsCommitWhenClean(t *testing.T) {
	ws := t.TempDir()
	repo := filepath.Join(ws, "svc")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	git := &fakeGit{statusBody: ""} // clean tree
	prov := &fakeProvider{host: "h", owner: "o"}
	var buf bytes.Buffer

	_, err := Push(context.Background(), Config{
		WorkspaceRoot: ws,
		RepoNames:     []string{"svc"},
		Provider:      prov,
		Git:           git,
		Out:           &buf,
	}, Options{Branch: "main", CommitMessage: "m"},
		ParsedBaseURL{Scheme: "https", Host: "h", Owner: "o"})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	// No `commit` in the calls because status was empty.
	for _, c := range git.calls {
		if len(c) >= 2 && c[1] == "commit" {
			t.Fatalf("unexpected commit call: %v", c)
		}
	}
	if !strings.Contains(buf.String(), "no changes to commit in svc") {
		t.Errorf("missing 'no changes' line: %q", buf.String())
	}
	// `git init` must be skipped when .git exists.
	for _, c := range git.calls {
		if len(c) >= 2 && c[1] == "init" {
			t.Fatalf("unexpected init on existing repo: %v", c)
		}
	}
}

func TestPushErrorsOnMissingRepoDir(t *testing.T) {
	ws := t.TempDir()
	git := &fakeGit{}
	prov := &fakeProvider{host: "h", owner: "o"}

	_, err := Push(context.Background(), Config{
		WorkspaceRoot: ws,
		RepoNames:     []string{"missing"},
		Provider:      prov,
		Git:           git,
		Out:           nil,
	}, Options{Branch: "main"}, ParsedBaseURL{Scheme: "https", Host: "h", Owner: "o"})
	if err == nil {
		t.Fatal("expected error for missing repo dir")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("err = %v", err)
	}
}
