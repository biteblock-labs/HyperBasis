package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnv(t *testing.T) {
	unsetEnv(t, "FOO")
	unsetEnv(t, "QUOTED")
	unsetEnv(t, "SINGLE")
	unsetEnv(t, "EMPTY")
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "" +
		"# comment\n" +
		"FOO=bar\n" +
		"QUOTED=\"baz\"\n" +
		"SINGLE='qux'\n" +
		"EMPTY=\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}
	if err := LoadEnv(path); err != nil {
		t.Fatalf("load env: %v", err)
	}
	if got := os.Getenv("FOO"); got != "bar" {
		t.Fatalf("FOO expected bar, got %q", got)
	}
	if got := os.Getenv("QUOTED"); got != "baz" {
		t.Fatalf("QUOTED expected baz, got %q", got)
	}
	if got := os.Getenv("SINGLE"); got != "qux" {
		t.Fatalf("SINGLE expected qux, got %q", got)
	}
	if got := os.Getenv("EMPTY"); got != "" {
		t.Fatalf("EMPTY expected empty, got %q", got)
	}
}

func TestLoadEnvDoesNotOverrideExisting(t *testing.T) {
	t.Setenv("FOO", "existing")
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("FOO=bar\n"), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}
	if err := LoadEnv(path); err != nil {
		t.Fatalf("load env: %v", err)
	}
	if got := os.Getenv("FOO"); got != "existing" {
		t.Fatalf("FOO expected existing, got %q", got)
	}
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	if old, ok := os.LookupEnv(key); ok {
		t.Cleanup(func() { _ = os.Setenv(key, old) })
	} else {
		t.Cleanup(func() { _ = os.Unsetenv(key) })
	}
	_ = os.Unsetenv(key)
}
