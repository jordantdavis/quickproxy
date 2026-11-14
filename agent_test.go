package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// agentRun runs quickproxy in the directory cwd with the home directory home.
func agentRun(t *testing.T, cwd, home string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Chdir(cwd)
	var out, errb bytes.Buffer
	code = run(args, &out, &errb)
	return code, out.String(), errb.String()
}

// mustInit runs args in a new project directory and fails the test on a
// nonzero exit code. It returns the project directory and the output.
func mustInit(t *testing.T, args ...string) (cwd, out string) {
	t.Helper()
	cwd = t.TempDir()
	code, out, errs := agentRun(t, cwd, t.TempDir(), args...)
	if code != 0 {
		t.Fatalf("%v: code %d, err %q", args, code, errs)
	}
	return cwd, out
}

func TestPrimePrintsGuide(t *testing.T) {
	_, out := mustInit(t, "prime")
	for _, want := range []string{
		"quickproxy get routes -o json", "quickproxy create route", "--overwrite",
		"quickproxy delete route", "/_health", "{id}", "X-Forwarded-Prefix",
		"loopback", "quickproxy VERB routes --help",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prime output has no %q:\n%s", want, out)
		}
	}
}

func TestPrimeRejectsArgs(t *testing.T) {
	code, _, errs := agentRun(t, t.TempDir(), t.TempDir(), "prime", "extra")
	if code != 2 {
		t.Fatalf("code %d, err %q", code, errs)
	}
}

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}

// primeHookCount counts the command hooks that run "quickproxy prime" at
// session start.
func primeHookCount(settings map[string]any) int {
	hooks, _ := settings["hooks"].(map[string]any)
	entries, _ := hooks["SessionStart"].([]any)
	n := 0
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		list, _ := entry["hooks"].([]any)
		for _, hk := range list {
			m, _ := hk.(map[string]any)
			if m["type"] == "command" && m["command"] == "quickproxy prime" {
				n++
			}
		}
	}
	return n
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInitCreatesLocalSettings(t *testing.T) {
	cwd, out := mustInit(t, "init")
	local := filepath.Join(cwd, ".claude", "settings.local.json")
	if !strings.Contains(out, "Added SessionStart hook to "+local) {
		t.Fatalf("init output:\n%s", out)
	}
	if n := primeHookCount(readSettings(t, local)); n != 1 {
		t.Fatalf("got %d prime hooks, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("settings.json exists, want only settings.local.json: %v", err)
	}
}

func TestInitSharedWritesSettingsJSON(t *testing.T) {
	cwd, _ := mustInit(t, "init", "--shared")
	if n := primeHookCount(readSettings(t, filepath.Join(cwd, ".claude", "settings.json"))); n != 1 {
		t.Fatalf("got %d prime hooks, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".claude", "settings.local.json")); !os.IsNotExist(err) {
		t.Fatalf("settings.local.json exists: %v", err)
	}
}

func TestInitIsIdempotent(t *testing.T) {
	cwd, _ := mustInit(t, "init")
	code, out, errs := agentRun(t, cwd, t.TempDir(), "init")
	if code != 0 || !strings.Contains(out, "already present") {
		t.Fatalf("second init: code %d, out %q, err %q", code, out, errs)
	}
	if n := primeHookCount(readSettings(t, filepath.Join(cwd, ".claude", "settings.local.json"))); n != 1 {
		t.Fatalf("got %d prime hooks, want 1", n)
	}
}

func TestInitKeepsExistingSettings(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, ".claude", "settings.local.json")
	writeFile(t, path, `{
  "permissions": {"allow": ["Bash(go test:*)"]},
  "hooks": {
    "SessionStart": [{"matcher": "startup", "hooks": [{"type": "command", "command": "other"}]}],
    "Stop": [{"hooks": [{"type": "command", "command": "bye"}]}]
  }
}`)
	if code, _, errs := agentRun(t, cwd, t.TempDir(), "init"); code != 0 {
		t.Fatalf("code %d, err %q", code, errs)
	}
	got := readSettings(t, path)
	if _, ok := got["permissions"]; !ok {
		t.Fatalf("permissions lost: %v", got)
	}
	hooks := got["hooks"].(map[string]any)
	if _, ok := hooks["Stop"]; !ok {
		t.Fatalf("Stop hook lost: %v", hooks)
	}
	b, _ := json.Marshal(hooks["SessionStart"])
	if !strings.Contains(string(b), `"other"`) {
		t.Fatalf("other SessionStart hook lost: %s", b)
	}
	if n := primeHookCount(got); n != 1 {
		t.Fatalf("got %d prime hooks, want 1", n)
	}
}

func TestInitRefusesUnexpectedFiles(t *testing.T) {
	for name, content := range map[string]string{
		"bad JSON":            `{"hooks": `,
		"hooks not an object": `{"hooks": []}`,
		"SessionStart string": `{"hooks": {"SessionStart": "quickproxy prime"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			cwd := t.TempDir()
			path := filepath.Join(cwd, ".claude", "settings.local.json")
			writeFile(t, path, content)
			if code, _, errs := agentRun(t, cwd, t.TempDir(), "init"); code != 1 || !strings.Contains(errs, "changed nothing") {
				t.Fatalf("code %d, err %q", code, errs)
			}
			if b, _ := os.ReadFile(path); string(b) != content {
				t.Fatalf("file changed to %q", b)
			}
		})
	}
}

func TestInitRefusesUserLevel(t *testing.T) {
	home := t.TempDir()
	code, _, errs := agentRun(t, home, home, "init")
	if code != 1 || !strings.Contains(errs, "user-level") {
		t.Fatalf("init in home directory: code %d, err %q", code, errs)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Fatalf(".claude created in home: %v", err)
	}
}

func TestInitRefusesUserLevelThroughSymlink(t *testing.T) {
	home := t.TempDir()
	link := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(home, link); err != nil {
		t.Skip(err)
	}
	if code, _, _ := agentRun(t, link, home, "init"); code != 1 {
		t.Fatalf("init through a symlink to home: code %d", code)
	}
}
