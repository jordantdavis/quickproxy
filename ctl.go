package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
)

// errUsage marks a command-line mistake. runCtl exits 2 for it, and 1 for
// other errors.
var errUsage = errors.New("usage error")

// errNoChange tells modifyConfig that the change did nothing, so that it does
// not write the file.
var errNoChange = errors.New("no change")

// routeResources are the names of the route resource, as in kubectl:
// plural, singular and short name.
var routeResources = map[string]bool{"routes": true, "route": true, "rt": true}

// ctlCommands lists the route subcommands. main also prints it in -h.
const ctlCommands = `  quickproxy get routes [PATTERN...] [-o table|json|name] [--no-headers] [-config PATH]
  quickproxy create route PATTERN TARGET [--overwrite] [-config PATH]
  quickproxy delete route PATTERN... [--ignore-not-found] [-config PATH]

Resource names: routes, route, rt.
`

const ctlUsage = "usage:\n" + ctlCommands

// runCtl runs the "get", "create" and "delete" subcommands, which read and
// change the route file. It returns the process exit code: 0 on success,
// 1 on an error, 2 on a usage error.
func runCtl(verb string, args []string, stdout, stderr io.Writer) int {
	fset := flag.NewFlagSet(verb, flag.ContinueOnError)
	fset.SetOutput(stderr)
	fset.Usage = func() {
		fmt.Fprint(stderr, ctlUsage, "\nflags for ", verb, ":\n")
		fset.PrintDefaults()
	}
	configFlag := fset.String("config", "", "path to the JSON route file (default $XDG_CONFIG_HOME/quickproxy/routes.json or ~/.config/quickproxy/routes.json)")

	var run func(path string, names []string) error
	switch verb {
	case "get":
		output := fset.String("o", "table", "output format: table, json or name")
		noHeaders := fset.Bool("no-headers", false, "do not print the table header")
		run = func(path string, names []string) error {
			return ctlGet(path, names, *output, *noHeaders, stdout, stderr)
		}
	case "create":
		overwrite := fset.Bool("overwrite", false, "replace the target if the route exists")
		run = func(path string, args []string) error {
			if len(args) != 2 {
				return fmt.Errorf("%w: create route needs PATTERN and TARGET", errUsage)
			}
			return ctlCreate(path, args[0], args[1], *overwrite, stdout)
		}
	case "delete":
		ignore := fset.Bool("ignore-not-found", false, "do not fail if a route does not exist")
		run = func(path string, names []string) error {
			if len(names) == 0 {
				return fmt.Errorf("%w: delete route needs at least one PATTERN", errUsage)
			}
			return ctlDelete(path, names, *ignore, stdout)
		}
	default:
		fmt.Fprintf(stderr, "error: unknown command %q\n%s", verb, ctlUsage)
		return 2
	}

	pos, err := parseInterleaved(fset, args)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return 2
	}
	if len(pos) == 0 {
		fmt.Fprintf(stderr, "error: you must give a resource type, for example: quickproxy %s routes\n", verb)
		return 2
	}
	if !routeResources[pos[0]] {
		fmt.Fprintf(stderr, "error: unknown resource type %q (known types: routes, route, rt)\n", pos[0])
		return 2
	}
	path, _, err := resolveConfigPath(*configFlag)
	if err == nil {
		err = run(path, pos[1:])
	}
	switch {
	case errors.Is(err, errUsage):
		fmt.Fprintf(stderr, "error: %s\n%s", strings.TrimPrefix(err.Error(), errUsage.Error()+": "), ctlUsage)
		return 2
	case err != nil:
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

// parseInterleaved parses flags that come before, between or after the
// positional arguments, as kubectl does, and returns the positionals.
// Everything after "--" is positional.
func parseInterleaved(fset *flag.FlagSet, args []string) ([]string, error) {
	var tail []string
	for i, a := range args {
		if a == "--" {
			args, tail = args[:i], args[i+1:]
			break
		}
	}
	var pos []string
	for {
		if err := fset.Parse(args); err != nil {
			return nil, err
		}
		args = fset.Args()
		if len(args) == 0 {
			return append(pos, tail...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// resolveConfigPath returns the -config value, or the default route file
// path when the value is empty. isDefault tells which one it returned.
func resolveConfigPath(flagValue string) (path string, isDefault bool, err error) {
	if flagValue != "" {
		return flagValue, false, nil
	}
	p, err := defaultConfigPath(os.Getenv, os.UserHomeDir)
	return p, true, err
}

// readConfig parses the route file without compiling it. A missing file
// gives an empty Config and an error that matches fs.ErrNotExist.
func readConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return &Config{Routes: map[string]string{}}, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if cfg.Routes == nil {
		cfg.Routes = map[string]string{}
	}
	return &cfg, nil
}

// saveConfig compiles the routes and, if they are valid, replaces the file
// atomically: it writes a temporary file in the same directory and renames
// it over path. watchConfig therefore never reads a partly written file.
func saveConfig(path string, cfg *Config) error {
	if _, err := NewTable(cfg.Routes); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	mode := fs.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no effect after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// modifyConfig runs change on the route file under an exclusive lock on
// path+".lock", then saves the result. The lock stops two concurrent
// commands from losing each other's change. It creates the file and its
// directory if they do not exist.
func modifyConfig(path string, change func(*Config) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock %s: %w", lock.Name(), err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	cfg, err := readConfig(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	switch err := change(cfg); {
	case errors.Is(err, errNoChange):
		return nil
	case err != nil:
		return err
	}
	return saveConfig(path, cfg)
}

// resourceName is the kubectl-style name of a route, e.g. "route/grafana".
func resourceName(pattern string) string {
	return "route/" + strings.TrimPrefix(pattern, "/")
}

func ctlGet(path string, names []string, output string, noHeaders bool, stdout, stderr io.Writer) error {
	if output != "table" && output != "json" && output != "name" {
		return fmt.Errorf("%w: unknown output format %q (use table, json or name)", errUsage, output)
	}
	cfg, err := readConfig(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	routes := cfg.Routes
	if len(names) > 0 {
		routes = map[string]string{}
		var missing []error
		for _, n := range names {
			t, ok := cfg.Routes[n]
			if !ok {
				missing = append(missing, fmt.Errorf("route %q not found", n))
				continue
			}
			routes[n] = t
		}
		if len(missing) > 0 {
			return errors.Join(missing...)
		}
	}

	if output == "json" {
		data, err := json.MarshalIndent(routes, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "%s\n", data)
		return err
	}
	if len(routes) == 0 {
		fmt.Fprintln(stderr, "No routes found.")
		return nil
	}
	patterns := make([]string, 0, len(routes))
	for p := range routes {
		patterns = append(patterns, p)
	}
	sort.Strings(patterns)
	if output == "name" {
		for _, p := range patterns {
			fmt.Fprintln(stdout, resourceName(p))
		}
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 3, ' ', 0)
	if !noHeaders {
		fmt.Fprintln(tw, "PATTERN\tTARGET")
	}
	for _, p := range patterns {
		fmt.Fprintf(tw, "%s\t%s\n", p, routes[p])
	}
	return tw.Flush()
}

func ctlCreate(path, pattern, target string, overwrite bool, stdout io.Writer) error {
	verb := "created"
	err := modifyConfig(path, func(cfg *Config) error {
		if _, exists := cfg.Routes[pattern]; exists {
			if !overwrite {
				return fmt.Errorf("route %q already exists (use --overwrite to replace it)", pattern)
			}
			verb = "replaced"
		}
		cfg.Routes[pattern] = target
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, resourceName(pattern), verb)
	return nil
}

func ctlDelete(path string, names []string, ignoreNotFound bool, stdout io.Writer) error {
	var deleted []string
	err := modifyConfig(path, func(cfg *Config) error {
		var missing []error
		for _, n := range names {
			if _, ok := cfg.Routes[n]; !ok {
				if !ignoreNotFound {
					missing = append(missing, fmt.Errorf("route %q not found", n))
				}
				continue
			}
			delete(cfg.Routes, n)
			deleted = append(deleted, n)
		}
		if len(missing) > 0 {
			return errors.Join(missing...)
		}
		if len(deleted) == 0 {
			return errNoChange
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, n := range deleted {
		fmt.Fprintln(stdout, resourceName(n), "deleted")
	}
	return nil
}
