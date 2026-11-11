// Command quickproxy listens on one local port (for `tailscale serve`) and
// reverse-proxies each path prefix to a different local app.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "update" {
		os.Exit(runUpdate(os.Args[2:], os.Stdout, os.Stderr))
	}

	configPath := flag.String("config", "", "path to the JSON route file (default $XDG_CONFIG_HOME/quickproxy/routes.json or ~/.config/quickproxy/routes.json)")
	listen := flag.String("listen", "", "listen address (overrides \"listen\" in the file; default 127.0.0.1:8080)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(versionString())
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Without -config, the default file is optional: if it is missing, start
	// with no routes and let watchConfig load it when it appears.
	optional := false
	if *configPath == "" {
		p, err := defaultConfigPath(os.Getenv, os.UserHomeDir)
		if err != nil {
			log.Error("load config", "err", err)
			os.Exit(1)
		}
		*configPath, optional = p, true
		// Create the directory, so that the user only has to add the file.
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			log.Warn("create config directory", "err", err)
		}
	}
	log.Info("config", "path", *configPath)

	cfg, table, err := LoadConfig(*configPath)
	switch {
	case optional && errors.Is(err, fs.ErrNotExist):
		log.Warn("no route file; starting with no routes", "path", *configPath)
		cfg = &Config{}
		table, _ = NewTable(nil)
	case err != nil:
		log.Error("load config", "err", err)
		os.Exit(1)
	}
	addr := cfg.Listen
	if *listen != "" {
		addr = *listen
	}
	if addr == "" {
		addr = "127.0.0.1:8080"
	}

	srv := NewServer(table, log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go watchConfig(ctx, *configPath, 2*time.Second, hup, func() {
		newCfg, t, err := LoadConfig(*configPath)
		if err != nil {
			log.Error("reload failed; keeping old routes", "err", err)
			return
		}
		srv.SetTable(t)
		log.Info("routes reloaded", "count", len(t.Routes()))
		if newCfg.Listen != cfg.Listen {
			log.Warn("listen address changed in file; restart to apply", "listen", newCfg.Listen)
		}
	})

	hs := &http.Server{Addr: addr, Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()

	for _, r := range table.Routes() {
		log.Info("route", "pattern", r.Pattern, "target", r.Target)
	}
	log.Info("listening", "addr", addr, "version", versionString())
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
	<-done
}
