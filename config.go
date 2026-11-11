package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Config is the JSON configuration file.
type Config struct {
	Listen string            `json:"listen"`
	Routes map[string]string `json:"routes"`
}

// defaultConfigPath returns $XDG_CONFIG_HOME/quickproxy/routes.json, or
// ~/.config/quickproxy/routes.json when XDG_CONFIG_HOME is unset or relative.
// It uses the same rule on macOS and Linux.
func defaultConfigPath(getenv func(string) string, home func() (string, error)) (string, error) {
	dir := getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(dir) {
		h, err := home()
		if err != nil {
			return "", fmt.Errorf("find default route file: %w", err)
		}
		dir = filepath.Join(h, ".config")
	}
	return filepath.Join(dir, "quickproxy", "routes.json"), nil
}

// LoadConfig reads, parses and compiles the configuration file.
func LoadConfig(path string) (*Config, *Table, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(cfg.Routes) == 0 {
		return nil, nil, errors.New(path + `: no "routes"`)
	}
	t, err := NewTable(cfg.Routes)
	if err != nil {
		return nil, nil, fmt.Errorf("%s:\n%w", path, err)
	}
	return &cfg, t, nil
}

// watchConfig calls reload when the file's mtime or size changes, or when
// a value arrives on hup. It returns when ctx is done.
func watchConfig(ctx context.Context, path string, every time.Duration, hup <-chan os.Signal, reload func()) {
	stamp := func() (time.Time, int64) {
		fi, err := os.Stat(path)
		if err != nil {
			return time.Time{}, -1
		}
		return fi.ModTime(), fi.Size()
	}
	lastMod, lastSize := stamp()
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			lastMod, lastSize = stamp()
			reload()
		case <-tick.C:
			mod, size := stamp()
			if size >= 0 && (!mod.Equal(lastMod) || size != lastSize) {
				lastMod, lastSize = mod, size
				reload()
			}
		}
	}
}
