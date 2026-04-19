// Package push publishes rendered gitups workspaces to a git provider
// (GitHub, GitLab, Gitea). The CLI wires flags into an Options and calls
// Push; unit tests inject GitRunner and a Provider stub so the core
// iteration logic stays free of network and shell calls.
package push

import (
	"fmt"
	"net/url"
	"strings"
)

// Options carries every user-visible knob for `gitups push`. CLI flags
// and credential resolution populate it before calling Push.
type Options struct {
	// Provider identifies the backend: "github", "gitlab", or "gitea".
	Provider string
	// BaseURL is the full HTTPS URL of the owner/group namespace,
	// e.g. https://github.com/myorg or https://gitea.example.com/team.
	// GitLab subgroups are expressed as /group/subgroup.
	BaseURL string
	// OwnerType is "org" or "user". Controls which create endpoint is
	// used on GitHub and Gitea. GitLab ignores it (namespace lookup
	// handles both).
	OwnerType string
	// Token authenticates provider REST calls and is injected into the
	// HTTPS push URL. Empty means "fall back to git credential helper"
	// and disables repo creation.
	Token string
	// User optionally overrides the HTTP basic-auth username injected
	// into the push URL. Defaults to a provider-appropriate literal
	// (e.g. "oauth2" for GitLab, "x-access-token" for GitHub/Gitea)
	// when Token is set but User is not.
	User string
	// Branch is the branch name to push. Defaults to "main" at the CLI.
	Branch string
	// CommitMessage is the message used when the working tree has
	// changes to commit. Defaults to a deterministic "gitups: sync
	// <name>" at the CLI.
	CommitMessage string
	// Visibility is passed to the provider on creation: "private",
	// "public", or "internal" (GitLab only).
	Visibility string
	// CreateMissing controls whether to call the provider API when a
	// repo is not found. Needs Token.
	CreateMissing bool
	// Flatten replaces "/" with "-" in each rendered repo name before
	// using it as the remote-side repo name. Needed for GitHub/Gitea
	// when the rendered name carries a slash.
	Flatten bool
	// Force passes --force to `git push`.
	Force bool
	// DryRun prints what would happen and runs `git push --dry-run` but
	// does not create remote repos or mutate provider state.
	DryRun bool
}

// ParsedBaseURL is the structured form of Options.BaseURL.
type ParsedBaseURL struct {
	// Scheme is "https" (or "http" for self-hosted plaintext; ssh not
	// supported — users needing ssh can rely on git credential helpers
	// via an https base-url and a credential helper that redirects).
	Scheme string
	// Host is the provider host, e.g. "github.com" or
	// "gitea.example.com:3000".
	Host string
	// Owner is the namespace path below the host, e.g. "myorg" or
	// "mygroup/subgroup" for GitLab.
	Owner string
}

// ParseBaseURL splits an HTTPS URL into (host, owner path). It rejects
// empty paths (no owner), query strings, and fragments so the user sees
// a useful error before any provider call is made.
func ParseBaseURL(raw string) (ParsedBaseURL, error) {
	if raw == "" {
		return ParsedBaseURL{}, fmt.Errorf("--base-url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ParsedBaseURL{}, fmt.Errorf("parse --base-url %q: %w", raw, err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return ParsedBaseURL{}, fmt.Errorf("--base-url %q: scheme must be https (or http for self-hosted)", raw)
	}
	if u.Host == "" {
		return ParsedBaseURL{}, fmt.Errorf("--base-url %q: host is empty", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return ParsedBaseURL{}, fmt.Errorf("--base-url %q: query and fragment are not supported", raw)
	}
	owner := strings.Trim(u.Path, "/")
	if owner == "" {
		return ParsedBaseURL{}, fmt.Errorf("--base-url %q: owner/group path is required (e.g. https://%s/myorg)", raw, u.Host)
	}
	return ParsedBaseURL{Scheme: u.Scheme, Host: u.Host, Owner: owner}, nil
}

// RemoteRepoName applies the flatten rule to a rendered repo name. Used
// when composing the remote-side name for providers without subgroups.
func RemoteRepoName(rendered string, flatten bool) string {
	if !flatten {
		return rendered
	}
	return strings.ReplaceAll(rendered, "/", "-")
}

// CloneURL builds the HTTPS git URL for a repo under the parsed owner.
// Credentials are NOT included — use InjectCredentials for that.
func (p ParsedBaseURL) CloneURL(repo string) string {
	return fmt.Sprintf("%s://%s/%s/%s.git", p.Scheme, p.Host, p.Owner, repo)
}

// InjectCredentials returns a git push URL with basic-auth creds baked
// into the userinfo. Tokens in URLs are visible in `ps` and crash
// dumps, so callers should prefer credential helpers when possible.
func InjectCredentials(rawURL, user, token string) (string, error) {
	if token == "" {
		return rawURL, nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("inject credentials: %w", err)
	}
	if user == "" {
		user = "x-access-token"
	}
	u.User = url.UserPassword(user, token)
	return u.String(), nil
}
