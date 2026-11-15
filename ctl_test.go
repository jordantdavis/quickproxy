package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ctl runs a route subcommand against the file at path.
func ctl(t *testing.T, path string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(append(args, "--config", path), &out, &errb)
	return code, out.String(), errb.String()
}

func readRoutes(t *testing.T, path string) *Config {
	t.Helper()
	cfg, tb, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(tb.Routes()) != len(cfg.Routes) {
		t.Fatalf("table has %d routes, file has %d", len(tb.Routes()), len(cfg.Routes))
	}
	return cfg
}

func TestCtlCreateNewFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "routes.json")
	code, out, errs := ctl(t, p, "create", "route", "/abc", "http://localhost:3001/alpha")
	if code != 0 || out != "route/abc created\n" {
		t.Fatalf("code %d, out %q, err %q", code, out, errs)
	}
	cfg := readRoutes(t, p)
	if cfg.Routes["/abc"] != "http://localhost:3001/alpha" || cfg.Listen != "" {
		t.Fatalf("got %+v", cfg)
	}
	data, _ := os.ReadFile(p)
	if strings.Contains(string(data), "listen") {
		t.Errorf("new file has an empty listen: %s", data)
	}
}

func TestCtlCreateOverwrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "routes.json")
	os.WriteFile(p, []byte(`{"listen":"127.0.0.1:9","routes":{"/abc":"http://h:1","/keep":"http://h:2"}}`), 0o600)
	before, _ := os.ReadFile(p)

	code, _, errs := ctl(t, p, "create", "route", "/abc", "http://h:3")
	if code != 1 || !strings.Contains(errs, `route "/abc" already exists (use --overwrite to replace it)`) {
		t.Fatalf("code %d, err %q", code, errs)
	}
	if after, _ := os.ReadFile(p); !bytes.Equal(before, after) {
		t.Fatal("file changed after a failed create")
	}

	// The flag comes after the positionals, as in kubectl.
	code, out, _ := ctl(t, p, "create", "route", "/abc", "http://h:3", "--overwrite")
	if code != 0 || out != "route/abc replaced\n" {
		t.Fatalf("code %d, out %q", code, out)
	}
	cfg := readRoutes(t, p)
	if cfg.Routes["/abc"] != "http://h:3" || cfg.Routes["/keep"] != "http://h:2" || cfg.Listen != "127.0.0.1:9" {
		t.Fatalf("got %+v", cfg)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
}

func TestCtlCreateInvalid(t *testing.T) {
	p := filepath.Join(t.TempDir(), "routes.json")
	os.WriteFile(p, []byte(`{"routes":{"/a/{y}":"http://h"}}`), 0o644)
	before, _ := os.ReadFile(p)
	for _, args := range [][]string{
		{"abc", "http://h"},
		{"/a/", "http://h"},
		{"/_health", "http://h"},
		{"/a/{x}", "http://h"},
		{"/b", "ftp://h"},
	} {
		code, _, errs := ctl(t, p, "create", "route", args[0], args[1])
		if code != 1 || errs == "" {
			t.Errorf("%v: code %d, err %q", args, code, errs)
		}
	}
	if after, _ := os.ReadFile(p); !bytes.Equal(before, after) {
		t.Fatal("file changed after an invalid create")
	}
}

func TestCtlDelete(t *testing.T) {
	p := filepath.Join(t.TempDir(), "routes.json")
	os.WriteFile(p, []byte(`{"listen":"127.0.0.1:9","routes":{"/a":"http://h","/b":"http://h"}}`), 0o644)

	if code, _, errs := ctl(t, p, "delete", "route", "/x"); code != 1 || !strings.Contains(errs, `route "/x" not found`) {
		t.Fatalf("missing: code %d, err %q", code, errs)
	}
	if code, out, _ := ctl(t, p, "delete", "route", "/x", "--ignore-not-found"); code != 0 || out != "" {
		t.Fatalf("ignore-not-found: code %d, out %q", code, out)
	}
	if code, out, _ := ctl(t, p, "delete", "rt", "/a"); code != 0 || out != "route/a deleted\n" {
		t.Fatalf("delete: code %d, out %q", code, out)
	}
	if cfg := readRoutes(t, p); len(cfg.Routes) != 1 || cfg.Listen != "127.0.0.1:9" {
		t.Fatalf("got %+v", cfg)
	}
	ctl(t, p, "delete", "routes", "/b")
	if cfg := readRoutes(t, p); len(cfg.Routes) != 0 {
		t.Fatalf("after last delete: %+v", cfg)
	}
}

func TestCtlDeleteMissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "routes.json")
	if code, _, _ := ctl(t, p, "delete", "route", "/a", "--ignore-not-found"); code != 0 {
		t.Fatalf("code %d", code)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("delete with no change created the file")
	}
}

func TestCtlGet(t *testing.T) {
	p := filepath.Join(t.TempDir(), "routes.json")
	if code, out, errs := ctl(t, p, "get", "routes"); code != 0 || out != "" || errs != "No routes found.\n" {
		t.Fatalf("missing file: code %d, out %q, err %q", code, out, errs)
	}

	os.WriteFile(p, []byte(`{"routes":{"/grafana":"http://127.0.0.1:3000","/a":"http://h:1"}}`), 0o644)
	want := "PATTERN    TARGET\n/a         http://h:1\n/grafana   http://127.0.0.1:3000\n"
	if code, out, _ := ctl(t, p, "get", "routes"); code != 0 || out != want {
		t.Errorf("table: code %d, got\n%s", code, out)
	}
	if _, out, _ := ctl(t, p, "get", "routes", "--no-headers"); strings.Contains(out, "PATTERN") {
		t.Errorf("no-headers: got\n%s", out)
	}
	if _, out, _ := ctl(t, p, "get", "routes", "-o", "name"); out != "route/a\nroute/grafana\n" {
		t.Errorf("name: got %q", out)
	}
	// Flags can also come before the resource name.
	if _, out, _ := ctl(t, p, "get", "--no-headers", "--output", "name", "rt"); out != "route/a\nroute/grafana\n" {
		t.Errorf("flags first: got %q", out)
	}
	// The single-dash form of older releases still works.
	if _, out, _ := ctl(t, p, "get", "routes", "-no-headers"); strings.Contains(out, "PATTERN") {
		t.Errorf("-no-headers: got\n%s", out)
	}
	_, out, _ := ctl(t, p, "get", "route", "/a", "-o=json")
	var m map[string]string
	if err := json.Unmarshal([]byte(out), &m); err != nil || len(m) != 1 || m["/a"] != "http://h:1" {
		t.Errorf("json: got %q, %v", out, err)
	}
	if code, _, errs := ctl(t, p, "get", "route", "/nope"); code != 1 || !strings.Contains(errs, `route "/nope" not found`) {
		t.Errorf("missing name: code %d, err %q", code, errs)
	}
	if code, _, _ := ctl(t, p, "get", "routes", "-o", "yaml"); code != 2 {
		t.Errorf("bad output format: code %d", code)
	}
}

func TestCtlUsageErrors(t *testing.T) {
	p := filepath.Join(t.TempDir(), "routes.json")
	for _, args := range [][]string{
		{"get"},
		{"get", "pods"},
		{"create", "route", "/a"},
		{"delete", "route"},
		{"get", "routes", "--bogus"},
		{"apply", "route", "/a", "http://h"},
		{"create", "route", "/a", "http://h", "extra"},
	} {
		if code, _, _ := ctl(t, p, args...); code != 2 {
			t.Errorf("%v: code %d, want 2", args, code)
		}
	}
	if code, _, _ := ctl(t, p, "get", "-h"); code != 0 {
		t.Errorf("-h: code %d, want 0", code)
	}
}

func TestCtlCreateConcurrent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "routes.json")
	const n = 20
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out, errb bytes.Buffer
			if code := run([]string{"create", "route", fmt.Sprintf("/r%d", i), "http://h", "--config", p}, &out, &errb); code != 0 {
				t.Errorf("create %d: %s", i, errb.String())
			}
		}()
	}
	wg.Wait()
	if cfg := readRoutes(t, p); len(cfg.Routes) != n {
		t.Fatalf("got %d routes, want %d", len(cfg.Routes), n)
	}
}

func TestCtlCreateFileTarget(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "routes.json")
	plan := filepath.Join(dir, "my plan.html")
	os.WriteFile(plan, []byte("<h1>plan</h1>"), 0o600)
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	code, out, errs := ctl(t, p, "create", "route", "/plan", "./my plan.html")
	want := (&url.URL{Scheme: "file", Path: plan}).String()
	if code != 0 || out != "route/plan created ("+want+")\n" {
		t.Fatalf("code %d, out %q, err %q", code, out, errs)
	}
	if got := readRoutes(t, p).Routes["/plan"]; got != want {
		t.Errorf("stored %q, want %q", got, want)
	}

	before, _ := os.ReadFile(p)
	for _, target := range []string{"./missing.html", "file://" + filepath.Join(dir, "missing")} {
		code, _, errs = ctl(t, p, "create", "route", "/missing", target)
		if code != 1 || !strings.Contains(errs, "nothing exists at") {
			t.Errorf("%s: code %d, err %q", target, code, errs)
		}
	}
	if after, _ := os.ReadFile(p); !bytes.Equal(before, after) {
		t.Error("file changed after a failed create")
	}
}

func TestCtlCreateTargetForms(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	p := filepath.Join(home, "routes.json")
	os.MkdirAll(filepath.Join(home, "project", "grafana"), 0o755)
	os.WriteFile(filepath.Join(home, "project", "plan.html"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "outside.html"), []byte("x"), 0o644)
	os.Symlink(filepath.Join(dir, "outside.html"), filepath.Join(home, "link.html"))
	t.Setenv("HOME", home)
	t.Chdir(filepath.Join(home, "project"))

	// A bare word or host:port is not a local path, even when a folder or
	// file of that name exists. It is a usage error.
	for _, target := range []string{"grafana", "127.0.0.1:3000", "plan.html", "."} {
		code, _, errs := ctl(t, p, "create", "route", "/g", target)
		if code != 2 || !strings.Contains(errs, "a local path that starts with / or ./") {
			t.Errorf("%s: code %d, err %q", target, code, errs)
		}
	}
	// A directory is refused, the home directory too.
	for _, target := range []string{"./grafana", "../", home, "/", "file://" + home} {
		code, _, errs := ctl(t, p, "create", "route", "/d", target)
		if code != 1 || !strings.Contains(errs, "is not a file") {
			t.Errorf("%s: code %d, err %q", target, code, errs)
		}
	}
	// A file outside home is refused, also through a symlink inside home.
	for _, target := range []string{filepath.Join(dir, "outside.html"), "../../outside.html", "../link.html"} {
		code, _, errs := ctl(t, p, "create", "route", "/o", target)
		if code != 1 || !strings.Contains(errs, "serves only files inside your home directory") {
			t.Errorf("%s: code %d, err %q", target, code, errs)
		}
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("route file written after failed creates: %v", err)
	}

	// A file inside home is accepted in every form.
	for i, target := range []string{"./plan.html", "../project/plan.html", filepath.Join(home, "project", "plan.html")} {
		if code, _, errs := ctl(t, p, "create", "route", fmt.Sprintf("/ok%d", i), target); code != 0 {
			t.Errorf("%s: code %d, err %q", target, code, errs)
		}
	}
}
