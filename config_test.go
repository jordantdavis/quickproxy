package main

import (
	"context"
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

	for _, bad := range []string{`{`, `{"routes":{}}`, `{"route":{"/a":"http://h"}}`, `{"routes":{"a":"http://h"}}`} {
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
