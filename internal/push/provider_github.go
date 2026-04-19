package push

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// githubProvider speaks the GitHub REST v3 API. Works against
// github.com (https://api.github.com) and GitHub Enterprise
// (https://<host>/api/v3) by inspecting the configured host.
type githubProvider struct {
	cfg ProviderConfig
}

func (p *githubProvider) Name() string { return "github" }

func (p *githubProvider) apiBase() string {
	if p.cfg.Base.Host == "github.com" {
		return "https://api.github.com"
	}
	return fmt.Sprintf("%s://%s/api/v3", p.cfg.Base.Scheme, p.cfg.Base.Host)
}

func (p *githubProvider) headers() map[string]string {
	h := map[string]string{"Accept": "application/vnd.github+json"}
	if p.cfg.Token != "" {
		h["Authorization"] = "Bearer " + p.cfg.Token
	}
	return h
}

func (p *githubProvider) EnsureRepo(ctx context.Context, owner, repo, visibility string, createMissing bool) (string, error) {
	checkURL := fmt.Sprintf("%s/repos/%s/%s", p.apiBase(), owner, repo)
	status, body, err := doJSON(ctx, p.cfg.HTTP, http.MethodGet, checkURL, p.headers(), nil, nil)
	if err != nil {
		return "", err
	}
	switch {
	case status == http.StatusOK:
		return p.cfg.Base.CloneURL(repo), nil
	case status == http.StatusNotFound:
		if !createMissing {
			return "", fmt.Errorf("github: repo %s/%s not found and --create-missing=false", owner, repo)
		}
		if p.cfg.Token == "" {
			return "", errMissingToken("github")
		}
		return p.create(ctx, owner, repo, visibility)
	default:
		return "", httpError("github", "GET "+checkURL, status, body)
	}
}

func (p *githubProvider) create(ctx context.Context, owner, repo, visibility string) (string, error) {
	reqBody := map[string]any{
		"name":       repo,
		"private":    visibility != "public",
		"visibility": normalizeGithubVisibility(visibility),
		"auto_init":  false,
	}
	var endpoint string
	switch strings.ToLower(p.cfg.OwnerType) {
	case "user":
		endpoint = fmt.Sprintf("%s/user/repos", p.apiBase())
	default:
		endpoint = fmt.Sprintf("%s/orgs/%s/repos", p.apiBase(), owner)
	}
	status, body, err := doJSON(ctx, p.cfg.HTTP, http.MethodPost, endpoint, p.headers(), reqBody, nil)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", httpError("github", "POST "+endpoint, status, body)
	}
	return p.cfg.Base.CloneURL(repo), nil
}

func normalizeGithubVisibility(v string) string {
	switch strings.ToLower(v) {
	case "public":
		return "public"
	case "internal":
		return "internal"
	default:
		return "private"
	}
}
