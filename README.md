# quickproxy

quickproxy listens on one local port and sends each request to a different local app, by path prefix. It is made to sit behind `tailscale serve 8080`. It uses only the Go standard library.

## Install

On Linux or macOS, run this command to install the latest release:

```sh
curl -fsSL https://raw.githubusercontent.com/jordantdavis/quickproxy/main/install.sh | sh
```

The script finds your OS and CPU architecture, downloads the matching archive, and compares it to `checksums.txt`. It puts `quickproxy` in `~/.local/bin`. If that folder is not in your `PATH`, the script tells you the line to add to your shell profile.

You can set two environment variables:

- `VERSION`: the release tag to install, for example `v1.0.0`. The default is the latest release.
- `INSTALL_DIR`: the folder for the binary. The default is `$HOME/.local/bin`.

## Build and run

1. In this folder, run `mise install` to get the Go version in `mise.toml`.
2. Run `go build -o quickproxy .`
3. Run `./quickproxy`. It creates `~/.config/quickproxy/` and starts with no routes.
4. Copy `routes.example.json` to `~/.config/quickproxy/routes.json` and edit the routes. quickproxy loads the file within 2 seconds.
5. On the same machine, run `tailscale serve 8080`.

Flags:

- `-config`: the path to the route file. The default is in your home directory. See "Route file location".
- `-listen`: the listen address. This overrides `listen` in the file. The default is `127.0.0.1:8080`.
- `-version`: print the version and exit.

## Release

The release workflow builds binaries for Linux and macOS, on amd64 and arm64. It runs when you push a tag that starts with `v`.

1. On `main`, run `git tag v1.0.0`.
2. Run `git push origin v1.0.0`.
3. When the workflow is done, open the Releases page of the repo on GitHub. Download the `.tar.gz` archive for your system. Use `checksums.txt` to make sure that the download is correct.

## Route file location

If you do not give `-config`, quickproxy uses `$XDG_CONFIG_HOME/quickproxy/routes.json`. If `XDG_CONFIG_HOME` is not set or is not an absolute path, quickproxy uses `~/.config/quickproxy/routes.json`. The rule is the same on macOS and Linux.

If the directory does not exist, quickproxy creates it. If the default file does not exist, quickproxy starts with no routes. It logs the path that it expects, and `/` and `/_health` work. When you create the file, quickproxy loads it within 2 seconds.

If you give `-config` and the file does not exist, quickproxy exits with an error. quickproxy does not read `./routes.json` automatically. To use it, run `./quickproxy -config routes.json`.

## Route file

```json
{
  "listen": "127.0.0.1:8080",
  "routes": {
    "/grafana": "http://127.0.0.1:3000",
    "/team/a/dashboard": "http://127.0.0.1:7000/dash",
    "/in/{val}/path": "http://127.0.0.1:5000/api/{val}"
  }
}
```

A key is a path prefix. It can have many segments. A segment in braces, such as `{val}`, is a parameter. A parameter matches any one segment. A value is the backend URL. It can have a path, and the path can use the parameters of its key.

quickproxy removes the matched prefix and adds the rest of the path to the backend URL. The query string does not change.

| Public request | Backend request |
|---|---|
| `/grafana/d/abc?x=1` | `http://127.0.0.1:3000/d/abc?x=1` |
| `/team/a/dashboard/` | `http://127.0.0.1:7000/dash/` |
| `/in/42/path/items` | `http://127.0.0.1:5000/api/42/items` |

Rules for keys:

- A key starts with `/` and does not end with `/`.
- A parameter is a whole segment. `/a/{x}` is correct. `/a/x{y}` is not.
- `/` and `/_health` are reserved.
- Two keys with the same shape, such as `/a/{x}` and `/a/{y}`, are an error.

If more than one key matches, the key with more segments wins. If two keys have the same number of segments, a literal segment wins over a parameter. For example, `/in/special/path` wins over `/in/{val}/path`.

## Behavior

- A request for the prefix with no trailing slash, such as `/grafana`, gets a 308 redirect to `/grafana/`.
- quickproxy rewrites backend `Location` headers and `Set-Cookie` paths, so that they point back into the public prefix.
- The backend gets the `X-Forwarded-Prefix` header, for example `/in/42/path`. Apps that support a base path can use it to build links.
- WebSockets and server-sent events pass through.
- `/` shows a list of the routes. `/_health` returns `ok`.
- If a backend is not running, the client gets a 502.

Some apps build absolute links such as `/static/app.js`. These links skip the prefix and fail. If this occurs, set the base path or root URL in the app to its prefix.

quickproxy keeps the `X-Forwarded-For`, `X-Forwarded-Host` and `X-Forwarded-Proto` headers that it receives. This is correct behind `tailscale serve`. Do not expose the port on a public network interface, because clients can then forge these headers.

## Reload

When the route file changes, quickproxy reads it again. It checks every 2 seconds. To reload immediately, send SIGHUP:

```sh
pkill -HUP quickproxy
```

If the new file has an error, quickproxy logs it and keeps the old routes. A change to `listen` needs a restart.

## Test

```sh
go test -race ./...
```
