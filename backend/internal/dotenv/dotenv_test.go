package dotenv

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
}

func TestLoadParsesTheFormat(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".env.local", `
# a comment
GITHUB_TOKEN=github_pat_abc123
export EXPORTED=yes
QUOTED="hello world"
SINGLE='literal $value'
PADDED=   trailing-space-trimmed   
`)
	chdir(t, dir)

	for _, k := range []string{"GITHUB_TOKEN", "EXPORTED", "QUOTED", "SINGLE", "PADDED"} {
		t.Setenv(k, "") // registered for cleanup
		os.Unsetenv(k)
	}

	if loaded := Load(".env.local"); len(loaded) != 1 {
		t.Fatalf("expected one file loaded, got %v", loaded)
	}

	cases := map[string]string{
		"GITHUB_TOKEN": "github_pat_abc123",
		"EXPORTED":     "yes",
		"QUOTED":       "hello world",
		"SINGLE":       "literal $value",
		// A stray trailing space in a pasted token is a very confusing bug.
		"PADDED": "trailing-space-trimmed",
	}
	for key, want := range cases {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// The environment must win, or a shell export and CI secrets could never
// override the file.
func TestLoadDoesNotOverrideTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".env.local", "GITHUB_TOKEN=from-file\n")
	chdir(t, dir)

	t.Setenv("GITHUB_TOKEN", "from-environment")
	Load(".env.local")

	if got := os.Getenv("GITHUB_TOKEN"); got != "from-environment" {
		t.Errorf("file overrode the environment: got %q", got)
	}
}

// Listing .env.local first must give it precedence over .env.
func TestEarlierFilesWin(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".env.local", "SHARED=local\n")
	write(t, dir, ".env", "SHARED=shared\nONLY_IN_ENV=yes\n")
	chdir(t, dir)

	t.Setenv("SHARED", "")
	os.Unsetenv("SHARED")
	t.Setenv("ONLY_IN_ENV", "")
	os.Unsetenv("ONLY_IN_ENV")

	if loaded := Load(".env.local", ".env"); len(loaded) != 2 {
		t.Fatalf("expected two files, got %v", loaded)
	}
	if got := os.Getenv("SHARED"); got != "local" {
		t.Errorf("SHARED = %q, want the .env.local value", got)
	}
	if got := os.Getenv("ONLY_IN_ENV"); got != "yes" {
		t.Errorf("ONLY_IN_ENV = %q; .env should still contribute", got)
	}
}

// `go run .` executes from backend/, so the repo-root file has to be found.
func TestLoadSearchesParentDirectories(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".env.local", "FOUND_FROM_PARENT=yes\n")
	nested := filepath.Join(root, "backend")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, nested)

	t.Setenv("FOUND_FROM_PARENT", "")
	os.Unsetenv("FOUND_FROM_PARENT")

	Load(".env.local")
	if os.Getenv("FOUND_FROM_PARENT") != "yes" {
		t.Error("did not find .env.local in the parent directory")
	}
}

func TestLoadIgnoresMissingFiles(t *testing.T) {
	chdir(t, t.TempDir())
	if loaded := Load(".env.local", ".env"); len(loaded) != 0 {
		t.Errorf("expected nothing loaded, got %v", loaded)
	}
}

// A '#' inside an unquoted value stays part of it: guessing wrong about a hash
// in a secret is worse than requiring comments on their own line.
func TestInlineHashIsNotTreatedAsAComment(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".env.local", "TOKEN=abc#def\n")
	chdir(t, dir)

	t.Setenv("TOKEN", "")
	os.Unsetenv("TOKEN")

	Load(".env.local")
	if got := os.Getenv("TOKEN"); got != "abc#def" {
		t.Errorf("TOKEN = %q, want abc#def", got)
	}
}
