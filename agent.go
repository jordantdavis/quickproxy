package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// primeCommand is the hook command that "quickproxy init" adds.
const primeCommand = "quickproxy prime"

const primeGuide = `# quickproxy: one local port, many apps by path prefix

quickproxy is a reverse proxy on one loopback port, by default 127.0.0.1:8080.
It sits behind "tailscale serve 8080". It sends each request to a local app by
path prefix. For example, /grafana/d/abc goes to http://127.0.0.1:3000/d/abc.

Route commands have the form quickproxy VERB RESOURCE [PATTERN...] [flags].
The verb is get, create or delete. The resource can be routes, route or rt.
Flags can come before or after the patterns.

## Read routes

- quickproxy get routes -o json         (a map of pattern to target)
- quickproxy get routes /grafana        (only these patterns)
- curl -s http://127.0.0.1:8080/_health (prints ok if the server runs)

## Change routes

- quickproxy create route PATTERN TARGET
- quickproxy create route PATTERN TARGET --overwrite   (replace a target)
- quickproxy delete route PATTERN...

A pattern starts with "/" and does not end with "/". A segment in braces, such
as {id}, matches any one segment, and the target path can use it:
quickproxy create route /in/{id}/path http://127.0.0.1:5000/api/{id}

"/" and "/_health" are reserved. A running server loads each change within 2
seconds, so a route change needs no restart. If a route is not valid, the
command changes nothing and prints the error.

## Behavior to know

- A request for the prefix without a trailing slash gets a 308 redirect to the
  prefix with a slash.
- The backend gets the path without the prefix, and the X-Forwarded-Prefix
  header. An app that builds absolute links, such as /static/app.js, needs its
  base path or root URL set to its prefix.
- If the backend is not running, the client gets a 502.
- A change to "listen" in the route file needs a restart of the server.

## Rules

- Ask the user before you run delete or create --overwrite. Other apps can
  depend on the route.
- Never make quickproxy listen on a public network interface. It trusts the
  X-Forwarded-* headers, so it is safe only on loopback.
- Edit routes with the commands, not by hand. The commands lock and validate
  the route file.

For all flags, run quickproxy VERB routes --help.
`

func newPrimeCmd(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "prime",
		Short: "Print instructions for AI agents that use quickproxy",
		Long: `Print a guide that tells an AI agent how to use quickproxy. The hook from
"quickproxy init" runs this command at the start of each Claude Code session.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			_, err := fmt.Fprint(stdout, primeGuide)
			return err
		},
	}
}

func newInitCmd(stdout io.Writer) *cobra.Command {
	var shared bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Add a Claude Code SessionStart hook that runs quickproxy prime in this project",
		Long: `Add a SessionStart hook to the Claude Code settings of the project in the
current directory. The hook runs "quickproxy prime", so each session starts with
the quickproxy guide. quickproxy must be on your PATH.

By default, the hook goes into .claude/settings.local.json, which is personal
and not committed. With --shared, it goes into .claude/settings.json.

quickproxy init never writes your user-level settings in ~/.claude. A second
run changes nothing. quickproxy init keeps all other settings, but it can
change the order of the keys in the file.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			if err := refuseUserLevel(cwd); err != nil {
				return err
			}
			name := "settings.local.json"
			if shared {
				name = "settings.json"
			}
			path := filepath.Join(cwd, ".claude", name)
			settings, err := readSettingsFile(path)
			if err != nil {
				return err
			}
			changed, err := addPrimeHook(settings)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			if !changed {
				fmt.Fprintf(stdout, "SessionStart hook already present in %s\n", path)
				return nil
			}
			if err := writeSettingsFile(path, settings); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "Added SessionStart hook to %s\n", path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&shared, "shared", false, "write .claude/settings.json, the file that teams commit")
	return cmd
}

// refuseUserLevel fails when dir is the home directory, because
// ~/.claude/settings.json is the user-level Claude Code settings file.
func refuseUserLevel(dir string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if samePath(dir, home) {
		return errors.New("the current directory is your home directory, so the file is your user-level Claude Code settings. Run quickproxy init in a project directory")
	}
	return nil
}

// samePath reports whether a and b name the same directory after symlinks.
func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// readSettingsFile returns the JSON object in path, or an empty object when
// the file does not exist.
func readSettingsFile(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var settings map[string]any
	if err := json.Unmarshal(b, &settings); err != nil {
		return nil, fmt.Errorf("%s: not valid JSON, so quickproxy init changed nothing: %w", path, err)
	}
	if settings == nil {
		settings = map[string]any{}
	}
	return settings, nil
}

func writeSettingsFile(path string, settings map[string]any) error {
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// addPrimeHook adds a SessionStart command hook for "quickproxy prime" to
// settings. It has no matcher, so it runs on startup, resume, clear and
// compact. It returns false when the hook is already present. It fails, and
// changes nothing, when hooks or hooks.SessionStart has an unexpected type.
func addPrimeHook(settings map[string]any) (bool, error) {
	hooks := map[string]any{}
	if raw, ok := settings["hooks"]; ok {
		m, isObject := raw.(map[string]any)
		if !isObject {
			return false, errors.New(`"hooks" is not a JSON object, so quickproxy init changed nothing`)
		}
		hooks = m
	}
	var entries []any
	if raw, ok := hooks["SessionStart"]; ok {
		list, isArray := raw.([]any)
		if !isArray {
			return false, errors.New(`"hooks.SessionStart" is not a JSON array, so quickproxy init changed nothing`)
		}
		entries = list
	}
	if hasPrimeHook(entries) {
		return false, nil
	}
	entry := map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": primeCommand}},
	}
	hooks["SessionStart"] = append(entries, entry)
	settings["hooks"] = hooks
	return true, nil
}

func hasPrimeHook(entries []any) bool {
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		list, _ := entry["hooks"].([]any)
		for _, h := range list {
			hook, _ := h.(map[string]any)
			if hook["type"] == "command" && hook["command"] == primeCommand {
				return true
			}
		}
	}
	return false
}
