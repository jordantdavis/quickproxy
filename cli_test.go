package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	// install.sh and older releases of "quickproxy update" run -version.
	for _, flag := range []string{"-version", "--version"} {
		var out, errb bytes.Buffer
		if code := run([]string{flag}, &out, &errb); code != 0 || strings.TrimSpace(out.String()) != versionString() {
			t.Errorf("%s: code %d, out %q, err %q", flag, code, out.String(), errb.String())
		}
	}
}

func TestRunUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"bogus"},
		{"--bogus"},
		{"update", "extra"},
		{"update", "--bogus"},
	} {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != 2 || !strings.Contains(errb.String(), "--help") {
			t.Errorf("%v: code %d, err %q", args, code, errb.String())
		}
	}
}

func TestRunHelp(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"-h"}, &out, &errb); code != 0 {
		t.Fatalf("code %d, err %q", code, errb.String())
	}
	for _, want := range []string{"update", "get", "create", "delete", "--config", "--listen"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help does not mention %q:\n%s", want, out.String())
		}
	}
}

func TestNormalizeArgs(t *testing.T) {
	root := newRootCmd(&bytes.Buffer{}, &bytes.Buffer{})
	got := normalizeArgs(root, []string{
		"-config", "a.json", "-listen=:9", "-o", "json", "-o=name", "-h",
		"-help", "-check", "-unknown", "/a", "--force", "--", "-config",
	})
	want := []string{
		"--config", "a.json", "--listen=:9", "-o", "json", "-o=name", "-h",
		"--help", "--check", "-unknown", "/a", "--force", "--", "-config",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}
