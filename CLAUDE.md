# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

quickproxy is a single-binary reverse proxy (a server that forwards requests to other servers). It listens on one loopback port and sends each request to a local app by path prefix. A route can also serve one local file. A separate front proxy, for example `tailscale serve 8080`, can expose the port. quickproxy does not depend on it. It uses the Go standard library and `github.com/spf13/cobra` (with its `pflag` dependency) for the command line. Do not add other third-party dependencies.

All code is in package `main` at the repository root. `README.md` is the user documentation for the route syntax and the proxy behavior. If you change behavior, update `README.md` and `ARCHITECTURE.md` too.

## Commands

The Go version comes from `go.mod`. If Go is not on the machine, run `mise install`.

```sh
go build -o quickproxy .           # build (the binary is gitignored)
go test -race ./...                # all tests, as CI runs them
go test -race -run TestMatch .     # one test
gofmt -l .                         # CI fails if this prints any file
go vet ./...
./quickproxy                       # run with ~/.config/quickproxy/routes.json (or $XDG_CONFIG_HOME)
./quickproxy --config routes.json  # run with a local copy of routes.example.json
```

CI (`.github/workflows/ci.yml`) runs gofmt, vet, `go test -race` and build. The release workflow runs on a `v*` tag. It reuses CI, then builds linux and darwin binaries for amd64 and arm64. It sets the version with `-ldflags "-X main.version=<tag>"`.

## Architecture

Read `ARCHITECTURE.md` before you change code. If you change the architecture, update it.

## Tests

Tests use `httptest`. `proxy_test.go` has an echo backend (`echoBackend`) and `newTestProxy`. `route_test.go` has `mustTable`. `ctl_test.go` has `ctl`, which calls `run` with `--config`. `cli_test.go` tests `run` and `normalizeArgs`. `agent_test.go` has `agentRun`, which sets `HOME` and the working directory. Use these helpers for new tests.
