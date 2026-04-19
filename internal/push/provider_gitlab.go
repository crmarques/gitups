package push

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// gitlabProvider speaks the GitLab REST v4 API. It resolves the
// namespace (group or user) by path, then creates the project under
// that namespace. Subgroups are expressed in the owner path
// ("group/subgroup") and passed through as-is.
type gitlabProvider struct {
	cfg ProviderConfig
}

func (p *gitlabProvider) Name() string { return "gitlab" }

func (p *gitlabProvider) apiBase() string {
	return fmt.Sprintf("%s://%s/api/v4", p.cfg.Base.Scheme, p.cfg.Base.Host)
}

func (p *gitlabProvider) headers() map[string]string {
	h := map[string]string{}
	if p.cfg.Token != "" {
		h["PRIVATE-TOKEN"] = p.cfg.Token
	}
	return h
}

func (p *gitlabProvider) EnsureRepo(ctx context.Context, owner, repo, visibility string, createMissing bool) (string, error) {
	fullPath := owner + "/" + repo
	checkURL := fmt.Sprintf("%s/projects/%s", p.apiBase(), url.PathEscape(fullPath))
	status, body, err := doJSON(ctx, p.cfg.HTTP, http.MethodGet, checkURL, p.headers(), nil, nil)
	if err != nil {
		return "", err
	}
	switch {
	case status == http.StatusOK:
		return p.cfg.Base.CloneURL(repo), nil
	case status == http.StatusNotFound:
		if !createMissing {
			return "", fmt.Errorf("gitlab: project %s not found and --create-missing=false", fullPath)
		}
		if p.cfg.Token == "" {
			return "", errMissingToken("gitlab")
		}
		return p.create(ctx, owner, repo, visibility)
	default:
		return "", httpError("gitlab", "GET "+checkURL, status, body)
	}
}

type gitlabNamespace struct {
	ID   int    `json:"id"`
	Path string `json:"path"`
}

func (p *gitlabProvider) create(ctx context.Context, owner, repo, visibility string) (string, error) {
	// GitLab needs a namespace_id, not a path, on POST /projects. The
	// /namespaces/:path lookup returns the id for both user and group
	// namespaces (including subgroups), which is why we don't need a
	// gitlab-specific owner-type flag.
	nsURL := fmt.Sprintf("%s/namespaces/%s", p.apiBase(), url.PathEscape(owner))
	var ns gitlabNamespace
	status, body, err := doJSON(ctx, p.cfg.HTTP, http.MethodGet, nsURL, p.headers(), nil, &ns)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", httpError("gitlab", "GET "+nsURL, status, body)
	}
	if ns.ID == 0 {
		return "", fmt.Errorf("gitlab: namespace %q resolved to empty id", owner)
	}
	reqBody := map[string]any{
		"path":         repo,
		"name":         repo,
		"namespace_id": ns.ID,
		"visibility":   normalizeGitlabVisibility(visibility),
	}
	createURL := fmt.Sprintf("%s/projects", p.apiBase())
	status, body, err = doJSON(ctx, p.cfg.HTTP, http.MethodPost, createURL, p.headers(), reqBody, nil)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", httpError("gitlab", "POST "+createURL, status, body)
	}
	return p.cfg.Base.CloneURL(repo), nil
}

func normalizeGitlabVisibility(v string) string {
	switch strings.ToLower(v) {
	case "public":
		return "public"
	case "internal":
		return "internal"
	default:
		return "private"
	}
}
