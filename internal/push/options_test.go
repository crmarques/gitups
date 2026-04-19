package push

import "testing"

func TestParseBaseURL(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantHost  string
		wantOwner string
		wantErr   bool
	}{
		{name: "github simple", in: "https://github.com/myorg", wantHost: "github.com", wantOwner: "myorg"},
		{name: "trailing slash", in: "https://github.com/myorg/", wantHost: "github.com", wantOwner: "myorg"},
		{name: "gitlab subgroup", in: "https://gitlab.example.com/group/sub", wantHost: "gitlab.example.com", wantOwner: "group/sub"},
		{name: "gitea self-hosted", in: "https://gitea.example.com:3000/team", wantHost: "gitea.example.com:3000", wantOwner: "team"},
		{name: "http plaintext", in: "http://gitea.local/team", wantHost: "gitea.local", wantOwner: "team"},
		{name: "empty", in: "", wantErr: true},
		{name: "no owner", in: "https://github.com", wantErr: true},
		{name: "ssh scheme rejected", in: "ssh://git@github.com/myorg", wantErr: true},
		{name: "query rejected", in: "https://github.com/myorg?foo=1", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseBaseURL(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("ParseBaseURL(%q): want error, got %+v", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseBaseURL(%q): unexpected error: %v", c.in, err)
			}
			if got.Host != c.wantHost {
				t.Errorf("host = %q, want %q", got.Host, c.wantHost)
			}
			if got.Owner != c.wantOwner {
				t.Errorf("owner = %q, want %q", got.Owner, c.wantOwner)
			}
		})
	}
}

func TestRemoteRepoName(t *testing.T) {
	cases := []struct {
		in, out string
		flatten bool
	}{
		{in: "basic-infra", out: "basic-infra"},
		{in: "platform/basic-infra", out: "platform/basic-infra"},
		{in: "platform/basic-infra", out: "platform-basic-infra", flatten: true},
		{in: "a/b/c", out: "a-b-c", flatten: true},
	}
	for _, c := range cases {
		got := RemoteRepoName(c.in, c.flatten)
		if got != c.out {
			t.Errorf("RemoteRepoName(%q, flatten=%v) = %q, want %q", c.in, c.flatten, got, c.out)
		}
	}
}

func TestCloneURL(t *testing.T) {
	p := ParsedBaseURL{Scheme: "https", Host: "github.com", Owner: "myorg"}
	got := p.CloneURL("basic-infra")
	want := "https://github.com/myorg/basic-infra.git"
	if got != want {
		t.Errorf("CloneURL = %q, want %q", got, want)
	}
	sub := ParsedBaseURL{Scheme: "https", Host: "gitlab.example.com", Owner: "group/sub"}
	got = sub.CloneURL("svc")
	want = "https://gitlab.example.com/group/sub/svc.git"
	if got != want {
		t.Errorf("CloneURL subgroup = %q, want %q", got, want)
	}
}

func TestInjectCredentials(t *testing.T) {
	// no token: unchanged
	got, err := InjectCredentials("https://github.com/myorg/repo.git", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://github.com/myorg/repo.git" {
		t.Errorf("no-token URL changed: %q", got)
	}
	// with token and default user
	got, err = InjectCredentials("https://github.com/myorg/repo.git", "", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://x-access-token:abc123@github.com/myorg/repo.git"
	if got != want {
		t.Errorf("default-user URL = %q, want %q", got, want)
	}
	// explicit user (e.g. oauth2 for gitlab)
	got, err = InjectCredentials("https://gitlab.example.com/g/r.git", "oauth2", "glpat-xyz")
	if err != nil {
		t.Fatal(err)
	}
	want = "https://oauth2:glpat-xyz@gitlab.example.com/g/r.git"
	if got != want {
		t.Errorf("explicit-user URL = %q, want %q", got, want)
	}
}
