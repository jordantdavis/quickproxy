# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

quickproxy is a single-binary reverse proxy (a server that forwards requests to other servers). It listens on one loopback port and sends each request to a local app by path prefix. A separate front proxy, for example `tailscale serve 8080`, can expose the port. quickproxy does not depend on it. It uses the Go standard library and `github.com/spf13/cobra` (with its `pflag` dependency) for the command line. Do not add other third-party dependencies.

All code is in package `main` at the repository root. `README.md` is the user documentation for the route syntax and the proxy behavior. If you change behavior, update `README.md` too.

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

The command line is in `cli.go`. `main` calls `run`, which builds a new Cobra command tree for each call. The root command runs `serve` (`main.go`). The request path is `serve` → `Server.ServeHTTP` (`proxy.go`) → `Table.Match` (`route.go`) → `httputil.ReverseProxy`.

- `route.go` holds the routing logic. `NewTable` compiles the JSON route map into a `Table`. It validates every pattern and target, and it returns all errors together. Duplicate shapes, such as `/a/{x}` and `/a/{y}`, are an error. The table is sorted by precedence: more segments first, then a literal segment before a parameter. `Match` returns the first route that matches.
- A `Match` carries `Prefix` (the public prefix with values filled in), `Base` (the backend base path) and `Rest` (the remainder). All three are in escaped (wire) form. Matching compares decoded segments, but the proxy sends escaped segments to the backend. When you edit path code, keep this split.
- `Match.publicPath` and `rewriteLocation` map backend paths back to the public prefix. `modifyResponse` in `proxy.go` uses them for `Location` headers and `Set-Cookie` paths.
- `proxy.go` stores the `*Match` in the request context under `ctxKey{}`. `rewrite`, `modifyResponse` and the `ErrorHandler` read it from there. `Rest == ""` gives a 308 redirect to the prefix with a trailing slash. `/` (the route index page) and `/_health` are served before matching. Requests with `.` or `..` segments get a 400.
- Without `--config`, `serve` uses `defaultConfigPath` (`config.go`): `$XDG_CONFIG_HOME/quickproxy/routes.json`, else `~/.config/quickproxy/routes.json`, on macOS and Linux. `serve` creates the directory of the default path. If that default file is missing, the server starts with an empty table, and `watchConfig` loads the file when it appears. A missing file given with `--config` is still an error.
- The route table is in an `atomic.Pointer`. `Server.SetTable` swaps it during live requests without locks. `watchConfig` in `config.go` polls the file mtime and size every 2 seconds. It also reloads on SIGHUP. If a reload fails, the server logs the error and keeps the old table. A change to `listen` needs a restart.
- `ReverseProxy.FlushInterval` is -1, and `statusWriter.Unwrap` exposes the real writer. Both are necessary for server-sent events and WebSocket upgrades. Do not remove them.
- The proxy keeps the inbound `X-Forwarded-For`, `X-Forwarded-Host` and `X-Forwarded-Proto` headers, because a local front proxy, such as `tailscale serve`, sets them. It also adds `X-Forwarded-Prefix`. This is safe only on loopback.
- `run` returns the exit code. A command returns an `exitError` to set the code. `usageError` gives exit code 2 and a hint to run `--help`. An error from Cobra before `PersistentPreRun` (an unknown command or flag) also gives 2. Another error from a command gives 1. `serve` logs its own errors and returns an `exitError` with no message.
- `normalizeArgs` changes the one-dash long flags of older releases, such as `-config`, to `--config`. It changes only flag names that a command defines. Do not remove it, because older releases of `update` and `install.sh` run the new binary with `-version`.
- `quickproxy update` is the Cobra command from `newUpdateCmd`. `update.go` does the same steps as `install.sh`: it finds the latest tag from the `releases/latest` redirect, compares the SHA-256 sum, and renames the new binary over `os.Executable()` after it runs `-version` on it. The smoke test uses the one-dash form, because `update --version` can install an older release that accepts only that form. The asset names must match `release.yml`. Every release must keep the `-version` flag, because `update` uses it as a smoke test. Its exit codes are 0, 1 (`--check` found a newer release) and 2 (error).
- `get`, `create` and `delete` are Cobra commands in `ctl.go`. They follow kubectl: `VERB RESOURCE [NAMES] [flags]`. `newVerbCmd` makes each verb with one `routes` subcommand that has the aliases `route` and `rt`. The flags are persistent flags on the verb, so that they can come before or after the resource name. `resolveConfigPath` gives the server and the subcommands one path rule. `modifyConfig` holds a `syscall.Flock` on `<path>.lock`, and `saveConfig` validates with `NewTable`, then writes a temp file and renames it over the route file. `LoadConfig` accepts an empty `routes` map, so that the last route can be deleted. Exit codes are 0, 1 (error) and 2 (usage).
- `prime` and `init` are in `agent.go`. `prime` prints `primeGuide`, a guide for AI agents. If you change route syntax or commands, update `primeGuide` too. `init` adds a Claude Code SessionStart hook that runs `quickproxy prime` to `.claude/settings.local.json`, or to `.claude/settings.json` with `--shared`. It keeps the other keys, it adds the hook only once, and it refuses to run in the home directory. If the file is not valid JSON or has an unexpected shape, it fails and writes nothing.
- `version.go` adds the short git revision from `debug.ReadBuildInfo` to `version`.

## Tests

Tests use `httptest`. `proxy_test.go` has an echo backend (`echoBackend`) and `newTestProxy`. `route_test.go` has `mustTable`. `ctl_test.go` has `ctl`, which calls `run` with `--config`. `cli_test.go` tests `run` and `normalizeArgs`. `agent_test.go` has `agentRun`, which sets `HOME` and the working directory. Use these helpers for new tests.
