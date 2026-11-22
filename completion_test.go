package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// Directive numbers from the Cobra completion protocol. The shell scripts
// read them from the last output line, after a colon.
const (
	directiveNoFileComp    = 4
	directiveFilterFileExt = 8
)

// complete runs "quickproxy __complete" as the shell scripts do. The last
// element of words is the word that the user completes. complete puts
// "--config path" before it, because Cobra completes the last argument.
// It returns the candidate lines, the directive line and stderr.
func complete(t *testing.T, path string, words ...string) (candidates []string, directive string, stderr string) {
	t.Helper()
	args := []string{"__complete"}
	args = append(args, words[:len(words)-1]...)
	if path != "" {
		args = append(args, "--config", path)
	}
	args = append(args, words[len(words)-1])
	var out, errb bytes.Buffer
	if code := run(args, &out, &errb); code != 0 {
		t.Fatalf("run %q: exit code %d, stderr %q", args, code, errb.String())
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, ":") {
		t.Fatalf("last output line %q is not a directive, output %q", last, out.String())
	}
	return lines[:len(lines)-1], last, errb.String()
}

func writeRouteFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "routes.json")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const completionRoutes = `{"routes": {
	"/docs/{x}": "http://127.0.0.1:3002",
	"/api": "http://127.0.0.1:3001",
	"/plan": "file:///home/u/plan.html"
}}`

func wantCompletion(t *testing.T, gotCands []string, gotDir string, wantCands []string, wantDir int) {
	t.Helper()
	if len(gotCands) == 0 {
		gotCands = nil
	}
	if !reflect.DeepEqual(gotCands, wantCands) {
		t.Errorf("candidates %q, want %q", gotCands, wantCands)
	}
	if want := ":" + strconv.Itoa(wantDir); gotDir != want {
		t.Errorf("directive %q, want %q", gotDir, want)
	}
}

func TestCompleteDeletePatterns(t *testing.T) {
	p := writeRouteFile(t, completionRoutes)
	cands, dir, _ := complete(t, p, "delete", "routes", "")
	wantCompletion(t, cands, dir, []string{
		"/api\thttp://127.0.0.1:3001",
		"/docs/{x}\thttp://127.0.0.1:3002",
		"/plan\tfile:///home/u/plan.html",
	}, directiveNoFileComp)
}

func TestCompleteSkipsGivenPatterns(t *testing.T) {
	p := writeRouteFile(t, completionRoutes)
	cands, dir, _ := complete(t, p, "get", "routes", "/api", "")
	wantCompletion(t, cands, dir, []string{
		"/docs/{x}\thttp://127.0.0.1:3002",
		"/plan\tfile:///home/u/plan.html",
	}, directiveNoFileComp)
}

func TestCompletePrefix(t *testing.T) {
	p := writeRouteFile(t, completionRoutes)
	cands, dir, _ := complete(t, p, "delete", "rt", "/do")
	wantCompletion(t, cands, dir, []string{"/docs/{x}\thttp://127.0.0.1:3002"}, directiveNoFileComp)
}

// The --config flag can come anywhere on the line, also in the one-dash form
// of older releases.
func TestCompleteConfigFlagPositions(t *testing.T) {
	p := writeRouteFile(t, completionRoutes)
	want := []string{"/api\thttp://127.0.0.1:3001"}
	for _, words := range [][]string{
		{"get", "--config", p, "routes", "/a"},
		{"get", "-config", p, "route", "/a"},
	} {
		cands, dir, _ := complete(t, "", words...)
		wantCompletion(t, cands, dir, want, directiveNoFileComp)
	}
}

// wantNoErrorText fails if stderr has any text other than the debug line
// that Cobra itself writes after each completion. The shell scripts discard
// stderr, but an error message there is still a bug.
func wantNoErrorText(t *testing.T, stderr string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSuffix(stderr, "\n"), "\n") {
		if line != "" && !strings.HasPrefix(line, "Completion ended with directive:") {
			t.Errorf("stderr %q, want only the Cobra directive line", stderr)
			return
		}
	}
}

func TestCompleteMissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "none.json")
	cands, dir, errs := complete(t, p, "delete", "routes", "")
	wantCompletion(t, cands, dir, nil, directiveNoFileComp)
	wantNoErrorText(t, errs)
}

func TestCompleteBadJSON(t *testing.T) {
	p := writeRouteFile(t, `{"routes": `)
	cands, dir, errs := complete(t, p, "get", "routes", "")
	wantCompletion(t, cands, dir, nil, directiveNoFileComp)
	wantNoErrorText(t, errs)
}

// A file that the server rejects still completes, so that the user can
// delete the bad route.
func TestCompleteInvalidRoutes(t *testing.T) {
	p := writeRouteFile(t, `{"routes": {
		"/a/{x}": "http://127.0.0.1:3001",
		"/a/{y}": "http://127.0.0.1:3002"
	}}`)
	if _, _, err := LoadConfig(p); err == nil {
		t.Fatal("LoadConfig accepted a duplicate shape")
	}
	cands, dir, _ := complete(t, p, "delete", "routes", "")
	wantCompletion(t, cands, dir, []string{
		"/a/{x}\thttp://127.0.0.1:3001",
		"/a/{y}\thttp://127.0.0.1:3002",
	}, directiveNoFileComp)
}

func TestCompleteConfigFlag(t *testing.T) {
	for _, words := range [][]string{
		{"--config", ""},
		{"get", "routes", "--config", ""},
		{"create", "--config", ""},
		{"delete", "route", "--config", ""},
	} {
		cands, dir, _ := complete(t, "", words...)
		wantCompletion(t, cands, dir, []string{"json"}, directiveFilterFileExt)
	}
}
