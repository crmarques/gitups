package push

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// giteaProvider speaks the Gitea REST v1 API. Shape mirrors the GitHub
// provider (owner-type toggles between /orgs and /user endpoints)
// because Gitea historically modelled itself on the GitHub API.
type giteaProvider struct {
	cfg ProviderConfig
}

func (p *giteaProvider) Name() string { return "gitea" }

func (p *giteaProvider) apiBase() string {
	return fmt.Sprintf("%s://%s/api/v1", p.cfg.Base.Scheme, p.cfg.Base.Host)
}

func (p *giteaProvider) headers() map[string]string {
	h := map[string]string{}
	if p.cfg.Token != "" {
		h["Authorization"] = "token " + p.cfg.Token
	}
	return h
}

func (p *giteaProvider) EnsureRepo(ctx context.Context, owner, repo, visibility string, createMissing bool) (string, error) {
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
			return "", fmt.Errorf("gitea: repo %s/%s not found and --create-missing=false", owner, repo)
		}
		if p.cfg.Token == "" {
			return "", errMissingToken("gitea")
		}
		return p.create(ctx, owner, repo, visibility)
	default:
		return "", httpError("gitea", "GET "+checkURL, status, body)
	}
}

func (p *giteaProvider) create(ctx context.Context, owner, repo, visibility string) (string, error) {
	reqBody := map[string]any{
		"name":           repo,
		"private":        strings.ToLower(visibility) != "public",
		"auto_init":      false,
		"default_branch": "main",
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
		return "", httpError("gitea", "POST "+endpoint, status, body)
	}
	return p.cfg.Base.CloneURL(repo), nil
}
