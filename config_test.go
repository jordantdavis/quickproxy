package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "routes.json")
	write := func(s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(`{"listen":"127.0.0.1:9999","routes":{"/a":"http://127.0.0.1:1"}}`)
	cfg, tb, err := LoadConfig(p)
	if err != nil || cfg.Listen != "127.0.0.1:9999" || len(tb.Routes()) != 1 {
		t.Fatalf("good config: %v %v", cfg, err)
	}

	write(`{"routes":{}}`)
	if _, tb, err := LoadConfig(p); err != nil || len(tb.Routes()) != 0 {
		t.Errorf("empty routes: got %v, want an empty table", err)
	}

	for _, bad := range []string{`{`, `{}`, `{"routes":null}`, `{"route":{"/a":"http://h"}}`, `{"routes":{"a":"http://h"}}`} {
		write(bad)
		if _, _, err := LoadConfig(p); err == nil {
			t.Errorf("%s: want error", bad)
		}
	}
}

func TestWatchConfigReloads(t *testing.T) {
	p := filepath.Join(t.TempDir(), "routes.json")
	os.WriteFile(p, []byte(`{"routes":{"/a":"http://h"}}`), 0o644)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hup := make(chan os.Signal, 1)
	reloads := make(chan struct{}, 4)
	go watchConfig(ctx, p, 10*time.Millisecond, hup, func() { reloads <- struct{}{} })

	time.Sleep(30 * time.Millisecond)
	os.WriteFile(p, []byte(`{"routes":{"/a":"http://h","/b":"http://h"}}`), 0o644)
	select {
	case <-reloads:
	case <-time.After(time.Second):
		t.Fatal("no reload after file change")
	}

	hup <- os.Interrupt
	select {
	case <-reloads:
	case <-time.After(time.Second):
		t.Fatal("no reload after SIGHUP")
	}
}

func TestDefaultConfigPath(t *testing.T) {
	home := func() (string, error) { return "/home/u", nil }
	for _, tc := range []struct{ xdg, want string }{
		{"/x", "/x/quickproxy/routes.json"},
		{"", "/home/u/.config/quickproxy/routes.json"},
		{"rel/dir", "/home/u/.config/quickproxy/routes.json"},
	} {
		getenv := func(k string) string {
			if k == "XDG_CONFIG_HOME" {
				return tc.xdg
			}
			return ""
		}
		got, err := defaultConfigPath(getenv, home)
		if err != nil || got != tc.want {
			t.Errorf("XDG_CONFIG_HOME=%q: got %q, %v; want %q", tc.xdg, got, err, tc.want)
		}
	}

	noHome := func() (string, error) { return "", errors.New("no home") }
	if _, err := defaultConfigPath(func(string) string { return "" }, noHome); err == nil {
		t.Error("no home: want error")
	}
}

func TestLoadConfigMissingIsNotExist(t *testing.T) {
	_, _, err := LoadConfig(filepath.Join(t.TempDir(), "missing.json"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("got %v, want fs.ErrNotExist", err)
	}
}

func TestWatchConfigFileAppears(t *testing.T) {
	p := filepath.Join(t.TempDir(), "routes.json")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloads := make(chan struct{}, 4)
	go watchConfig(ctx, p, 10*time.Millisecond, make(chan os.Signal), func() { reloads <- struct{}{} })

	time.Sleep(30 * time.Millisecond)
	os.WriteFile(p, []byte(`{"routes":{"/a":"http://h"}}`), 0o644)
	select {
	case <-reloads:
	case <-time.After(time.Second):
		t.Fatal("no reload after file appeared")
	}
}
