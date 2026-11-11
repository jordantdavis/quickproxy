# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

quickproxy is a single-binary reverse proxy (a server that forwards requests to other servers). It listens on one loopback port and sends each request to a local app by path prefix. It sits behind `tailscale serve 8080`. It uses only the Go standard library. Do not add third-party dependencies.

All code is in package `main` at the repository root. `README.md` is the user documentation for the route syntax and the proxy behavior. If you change behavior, update `README.md` too.

## Commands

The Go version comes from `go.mod`. If Go is not on the machine, run `mise install`.

```sh
go build -o quickproxy .           # build (the binary is gitignored)
go test -race ./...                # all tests, as CI runs them
go test -race -run TestMatch .     # one test
gofmt -l .                         # CI fails if this prints any file
go vet ./...
./quickproxy -config routes.json   # run, after copying routes.example.json to routes.json
```

CI (`.github/workflows/ci.yml`) runs gofmt, vet, `go test -race` and build. The release workflow runs on a `v*` tag. It reuses CI, then builds linux and darwin binaries for amd64 and arm64. It sets the version with `-ldflags "-X main.version=<tag>"`.

## Architecture

The request path is `main.go` → `Server.ServeHTTP` (`proxy.go`) → `Table.Match` (`route.go`) → `httputil.ReverseProxy`.

- `route.go` holds the routing logic. `NewTable` compiles the JSON route map into a `Table`. It validates every pattern and target, and it returns all errors together. Duplicate shapes, such as `/a/{x}` and `/a/{y}`, are an error. The table is sorted by precedence: more segments first, then a literal segment before a parameter. `Match` returns the first route that matches.
- A `Match` carries `Prefix` (the public prefix with values filled in), `Base` (the backend base path) and `Rest` (the remainder). All three are in escaped (wire) form. Matching compares decoded segments, but the proxy sends escaped segments to the backend. When you edit path code, keep this split.
- `Match.publicPath` and `rewriteLocation` map backend paths back to the public prefix. `modifyResponse` in `proxy.go` uses them for `Location` headers and `Set-Cookie` paths.
- `proxy.go` stores the `*Match` in the request context under `ctxKey{}`. `rewrite`, `modifyResponse` and the `ErrorHandler` read it from there. `Rest == ""` gives a 308 redirect to the prefix with a trailing slash. `/` (the route index page) and `/_health` are served before matching. Requests with `.` or `..` segments get a 400.
- The route table is in an `atomic.Pointer`. `Server.SetTable` swaps it during live requests without locks. `watchConfig` in `config.go` polls the file mtime and size every 2 seconds. It also reloads on SIGHUP. If a reload fails, the server logs the error and keeps the old table. A change to `listen` needs a restart.
- `ReverseProxy.FlushInterval` is -1, and `statusWriter.Unwrap` exposes the real writer. Both are necessary for server-sent events and WebSocket upgrades. Do not remove them.
- The proxy keeps the inbound `X-Forwarded-For`, `X-Forwarded-Host` and `X-Forwarded-Proto` headers, because Tailscale sets them. It also adds `X-Forwarded-Prefix`. This is safe only on loopback.
- `quickproxy update` is a subcommand that `main` handles before `flag.Parse`. `update.go` does the same steps as `install.sh`: it finds the latest tag from the `releases/latest` redirect, compares the SHA-256 sum, and renames the new binary over `os.Executable()` after it runs `-version` on it. The asset names must match `release.yml`. Every release must keep the `-version` flag, because `update` uses it as a smoke test.
- `version.go` adds the short git revision from `debug.ReadBuildInfo` to `version`.

## Tests

Tests use `httptest`. `proxy_test.go` has an echo backend (`echoBackend`) and `newTestProxy`. `route_test.go` has `mustTable`. Use these helpers for new tests.
