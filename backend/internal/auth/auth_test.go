package auth

import "testing"

func TestSafeRedirectRejectsOffSiteDestinations(t *testing.T) {
	hostile := []string{
		"//evil.example.com",
		`/\evil.example.com`,
		`/\/evil.example.com`,
		"https://evil.example.com",
		"http://evil.example.com/path",
		"javascript:alert(1)",
		"evil.example.com",
		"/\tevil",
		"/\nevil",
		"",
	}
	for _, raw := range hostile {
		if got := safeRedirect(raw); got != "/" {
			t.Errorf("safeRedirect(%q) = %q, want \"/\"", raw, got)
		}
	}
}

func TestSafeRedirectKeepsLocalPaths(t *testing.T) {
	ok := []string{"/", "/sources", "/goals", "/sources?tab=github", "/a/b/c#frag"}
	for _, raw := range ok {
		if got := safeRedirect(raw); got != raw {
			t.Errorf("safeRedirect(%q) = %q, want it unchanged", raw, got)
		}
	}
}
