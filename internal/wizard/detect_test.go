package wizard

import "testing"

func TestParseGitHubRemoteURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"ssh", "git@github.com:kunchenguid/ezoss.git", "kunchenguid/ezoss"},
		{"ssh no .git", "git@github.com:kunchenguid/ezoss", "kunchenguid/ezoss"},
		{"ssh proto", "ssh://git@github.com/kunchenguid/ezoss.git", "kunchenguid/ezoss"},
		{"https", "https://github.com/kunchenguid/ezoss.git", "kunchenguid/ezoss"},
		{"https no .git", "https://github.com/kunchenguid/ezoss", "kunchenguid/ezoss"},
		{"http", "http://github.com/kunchenguid/ezoss", "kunchenguid/ezoss"},
		{"trailing slash", "https://github.com/kunchenguid/ezoss/", "kunchenguid/ezoss"},
		{"surrounding whitespace", "  git@github.com:kunchenguid/ezoss.git  ", "kunchenguid/ezoss"},
		{"non-github", "git@gitlab.com:foo/bar.git", ""},
		{"non-github https", "https://bitbucket.org/foo/bar.git", ""},
		{"empty", "", ""},
		{"missing name", "git@github.com:kunchenguid/", ""},
		{"missing owner", "git@github.com:/ezoss.git", ""},
		{"extra path segment", "https://github.com/kunchenguid/ezoss/tree/main", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseGitHubRemoteURL(tc.in)
			if got != tc.want {
				t.Fatalf("ParseGitHubRemoteURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
