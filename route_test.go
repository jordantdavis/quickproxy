package main

import (
	"strings"
	"testing"
)

func mustTable(t *testing.T, m map[string]string) *Table {
	t.Helper()
	tb, err := NewTable(m)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func TestMatch(t *testing.T) {
	tb := mustTable(t, map[string]string{
		"/grafana":          "http://127.0.0.1:3000",
		"/team":             "http://127.0.0.1:7001",
		"/team/a/dashboard": "http://127.0.0.1:7000/dash/",
		"/in/{val}/path":    "http://127.0.0.1:5000/api/{val}",
		"/in/special/path":  "http://127.0.0.1:5001",
		"/u/{id}/f/{name}":  "http://127.0.0.1:6000/s/{id}/x-{name}.d",
		"/enc/{v}":          "http://127.0.0.1:6001/e/{v}",
	})
	tests := []struct {
		path, pattern, prefix, backend string
	}{
		{"/grafana/d/abc", "/grafana", "/grafana", "/d/abc"},
		{"/grafana/", "/grafana", "/grafana", "/"},
		{"/team/a/dashboard/x", "/team/a/dashboard", "/team/a/dashboard", "/dash/x"},
		{"/team/a/x", "/team", "/team", "/a/x"}, // longer pattern does not match, shorter does
		{"/team/abc/", "/team", "/team", "/abc/"},
		{"/in/42/path/items", "/in/{val}/path", "/in/42/path", "/api/42/items"},
		{"/in/special/path/q", "/in/special/path", "/in/special/path", "/q"}, // literal beats param
		{"/u/7/f/doc/a", "/u/{id}/f/{name}", "/u/7/f/doc", "/s/7/x-doc.d/a"},
		{"/enc/a%2Fb/c", "/enc/{v}", "/enc/a%2Fb", "/e/a%2Fb/c"}, // escaped slash stays escaped
	}
	for _, tc := range tests {
		m, ok := tb.Match(tc.path)
		if !ok {
			t.Errorf("%s: no match", tc.path)
			continue
		}
		if m.Route.Pattern != tc.pattern || m.Prefix != tc.prefix || m.BackendPath() != tc.backend {
			t.Errorf("%s: got (%s, %s, %s), want (%s, %s, %s)", tc.path,
				m.Route.Pattern, m.Prefix, m.BackendPath(), tc.pattern, tc.prefix, tc.backend)
		}
	}

	for _, p := range []string{"/grafanax/", "/in//path/", "/in/42/other/", "/nope", "/teamx"} {
		if m, ok := tb.Match(p); ok {
			t.Errorf("%s: unexpected match %s", p, m.Route.Pattern)
		}
	}

	// Exact prefix with no trailing slash: Rest is empty so the server redirects.
	if m, _ := tb.Match("/in/42/path"); m == nil || m.Rest != "" {
		t.Errorf("exact prefix: want match with empty Rest, got %+v", m)
	}
}

func TestNewTableErrors(t *testing.T) {
	bad := map[string]map[string]string{
		"same shape":      {"/a/{x}": "http://h", "/a/{y}": "http://h"},
		"unknown param":   {"/a/{x}": "http://h/{y}"},
		"trailing slash":  {"/a/": "http://h"},
		"no leading":      {"a": "http://h"},
		"root":            {"/": "http://h"},
		"empty segment":   {"/a//b": "http://h"},
		"dot segment":     {"/a/../b": "http://h"},
		"partial param":   {"/a/x{y}": "http://h"},
		"dup param":       {"/{x}/{x}": "http://h"},
		"bad scheme":      {"/a": "ftp://h"},
		"no host":         {"/a": "http:///x"},
		"query in target": {"/a": "http://h/?q=1"},
		"health":          {"/_health": "http://h"},
	}
	for name, m := range bad {
		if _, err := NewTable(m); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestRewriteLocation(t *testing.T) {
	tb := mustTable(t, map[string]string{
		"/g":             "http://127.0.0.1:3000",
		"/in/{val}/path": "http://127.0.0.1:5000/api/{val}",
	})
	g, _ := tb.Match("/g/x")
	in, _ := tb.Match("/in/42/path/x")
	tests := []struct {
		m        *Match
		loc, out string
	}{
		{g, "/login?next=/", "/g/login?next=/"},
		{g, "http://127.0.0.1:3000/a#f", "/g/a#f"},
		{g, "https://example.com/a", "https://example.com/a"},
		{g, "relative", "relative"},
		{g, "//evil.com/a", "//evil.com/a"},
		{in, "/api/42/done", "/in/42/path/done"},
		{in, "/api/42", "/in/42/path"},
		{in, "/api/42?x=1", "/in/42/path?x=1"},
		{in, "/api/420", "/api/420"},
		{in, "/other", "/other"},
	}
	for _, tc := range tests {
		if got := tc.m.rewriteLocation(tc.loc); got != tc.out {
			t.Errorf("%s via %s: got %q, want %q", tc.loc, tc.m.Route.Pattern, got, tc.out)
		}
	}
}

func TestOrderIsDeterministic(t *testing.T) {
	tb := mustTable(t, map[string]string{"/b": "http://h", "/a": "http://h", "/{x}": "http://h", "/a/b": "http://h"})
	var got []string
	for _, r := range tb.Routes() {
		got = append(got, r.Pattern)
	}
	if s := strings.Join(got, " "); s != "/a/b /a /b /{x}" {
		t.Errorf("order: %s", s)
	}
}
