// Package dotenv loads key/value files into the process environment.
//
// Hand-written rather than pulled from a library, because the format this
// project needs is small and the failure mode of a clever parser is bad: a
// value silently mangled by quote or comment handling shows up much later as an
// unexplained 401. The rules here are deliberately dull and documented below.
package dotenv

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// searchDepth is how many parent directories are checked. Two is enough to
// cover `go run .` from backend/ finding a file at the repository root.
const searchDepth = 2

// Load reads each named file, if present, and sets any variables it defines
// that are not already in the environment.
//
// Real environment variables always win. That ordering is what lets a shell
// export, a CI secret or a Compose environment block override the file without
// anyone having to edit it — and it means listing ".env.local" before ".env"
// gives the local file precedence.
//
// Returns the paths actually loaded, so the caller can say what it read. It
// never returns the values, and nothing here should ever log one.
func Load(names ...string) []string {
	var loaded []string
	for _, name := range names {
		path, ok := findUp(name)
		if !ok {
			continue
		}
		if err := loadFile(path); err != nil {
			// A malformed config file should not stop the server from starting
			// when the environment may already carry everything needed.
			fmt.Fprintf(os.Stderr, "dotenv: ignoring %s: %v\n", path, err)
			continue
		}
		loaded = append(loaded, path)
	}
	return loaded
}

func findUp(name string) (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for i := 0; i <= searchDepth; i++ {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}

func loadFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		key, value, ok := parseLine(scanner.Text())
		if !ok {
			continue
		}
		if key == "" {
			return fmt.Errorf("line %d: missing variable name", line)
		}
		// Already set wins, so the file never clobbers a deliberate override.
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// parseLine handles `KEY=value`, an optional `export ` prefix, whole-line
// comments, and single- or double-quoted values.
//
// Inline comments are NOT stripped from unquoted values. Trailing `# ...` on a
// value line stays part of the value, because guessing wrong about a `#` inside
// a secret is worse than requiring comments on their own line. Surrounding
// whitespace is trimmed, since a stray trailing space in a pasted token is a
// genuinely common and very confusing mistake.
func parseLine(raw string) (key, value string, ok bool) {
	line := strings.TrimSpace(raw)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")

	key, value, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)

	if len(value) >= 2 {
		switch {
		case strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`):
			value = strings.NewReplacer(`\n`, "\n", `\"`, `"`, `\\`, `\`).
				Replace(value[1 : len(value)-1])
		case strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'"):
			// Single quotes are literal, as in a shell.
			value = value[1 : len(value)-1]
		}
	}
	return key, value, true
}
