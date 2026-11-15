package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// errNoChange tells modifyConfig that the change did nothing, so that it does
// not write the file.
var errNoChange = errors.New("no change")

// routeAliases are the other names of the "routes" resource, as in kubectl:
// singular and short name.
var routeAliases = []string{"route", "rt"}

// newVerbCmd returns a kubectl-style verb, such as "get", with the routes
// resource as its only subcommand. The verb holds the flags as persistent
// flags, so that they can come before or after the resource name.
func newVerbCmd(verb, short string, routes *cobra.Command) (*cobra.Command, *string) {
	configPath := new(string)
	cmd := &cobra.Command{
		Use:   verb + " RESOURCE",
		Short: short,
		Long: short + `.

The resource type can be routes, route or rt.`,
		// A verb without a known resource is a usage error.
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return usageError("you must give a resource type, for example: quickproxy %s routes", verb)
			}
			return usageError("unknown resource type %q (known types: routes, route, rt)", args[0])
		},
	}
	cmd.PersistentFlags().StringVar(configPath, "config", "", configFlagUsage)
	routes.Aliases = routeAliases
	cmd.AddCommand(routes)
	return cmd, configPath
}

func newGetCmd(stdout, stderr io.Writer) *cobra.Command {
	var output string
	var noHeaders bool
	var configPath *string
	routes := &cobra.Command{
		Use:   "routes [PATTERN...]",
		Short: "Show all routes, or only the routes with the given patterns",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, _, err := resolveConfigPath(*configPath)
			if err != nil {
				return err
			}
			return ctlGet(path, args, output, noHeaders, stdout, stderr)
		},
	}
	cmd, configPath := newVerbCmd("get", "Show routes from the route file", routes)
	f := cmd.PersistentFlags()
	f.StringVarP(&output, "output", "o", "table", "output format: table, json or name")
	f.BoolVar(&noHeaders, "no-headers", false, "do not print the table header")
	return cmd
}

func newCreateCmd(stdout io.Writer) *cobra.Command {
	var overwrite bool
	var configPath *string
	routes := &cobra.Command{
		Use:   "routes PATTERN TARGET",
		Short: "Add a route to the route file",
		Long: `Add a route to the route file.

TARGET is an http or https URL of a backend, or a local file.
Give a local target as a path that starts with / or ./, such as ./plan.html,
or as a file:// URL. The command stores the absolute file:// URL. It fails if
nothing exists at the path. The path must be a file, not a directory, inside
your home directory.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 2 {
				return usageError("create route needs PATTERN and TARGET")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			path, _, err := resolveConfigPath(*configPath)
			if err != nil {
				return err
			}
			return ctlCreate(path, args[0], args[1], overwrite, stdout)
		},
	}
	cmd, configPath := newVerbCmd("create", "Add a route to the route file", routes)
	cmd.PersistentFlags().BoolVar(&overwrite, "overwrite", false, "replace the target if the route exists")
	return cmd
}

func newDeleteCmd(stdout io.Writer) *cobra.Command {
	var ignoreNotFound bool
	var configPath *string
	routes := &cobra.Command{
		Use:   "routes PATTERN...",
		Short: "Remove routes from the route file",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return usageError("delete route needs at least one PATTERN")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			path, _, err := resolveConfigPath(*configPath)
			if err != nil {
				return err
			}
			return ctlDelete(path, args, ignoreNotFound, stdout)
		},
	}
	cmd, configPath := newVerbCmd("delete", "Remove routes from the route file", routes)
	cmd.PersistentFlags().BoolVar(&ignoreNotFound, "ignore-not-found", false, "do not fail if a route does not exist")
	return cmd
}

// resolveConfigPath returns the --config value, or the default route file
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
		return usageError("unknown output format %q (use table, json or name)", output)
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

// normalizeTarget turns a local path, such as ./plan.html, into an absolute
// file:// URL. A local path must start with /, ./ or ../, so that a backend
// without a scheme, such as grafana or 127.0.0.1:3000, is an error and not a
// file route. A file target must be a regular file inside the home
// directory. It returns http and https targets unchanged.
func normalizeTarget(target string) (string, error) {
	u, err := url.Parse(target)
	switch {
	case isLocalPath(target):
		abs, err := filepath.Abs(target)
		if err != nil {
			return "", err
		}
		u = &url.URL{Scheme: "file", Path: abs}
		target = u.String()
	case err != nil || u.Scheme == "":
		return "", usageError("target %q: give a URL with http://, https:// or file://, or a local path that starts with / or ./", target)
	case u.Scheme != "file":
		return target, nil
	}
	if !strings.HasPrefix(u.Path, "/") {
		return target, nil // NewTable reports the error
	}
	fi, err := os.Stat(u.Path)
	switch {
	case err != nil:
		return "", fmt.Errorf("target %q: nothing exists at %s", target, u.Path)
	case !fi.Mode().IsRegular():
		return "", fmt.Errorf("target %q: %s is not a file (a file route serves one file, not a directory)", target, u.Path)
	}
	if err := requireUnderHome(u.Path); err != nil {
		return "", fmt.Errorf("target %q: %w", target, err)
	}
	return target, nil
}

// isLocalPath reports whether a create target is written as a local path.
func isLocalPath(target string) bool {
	for _, p := range []string{"/", "./", "../"} {
		if strings.HasPrefix(target, p) {
			return true
		}
	}
	return false
}

// requireUnderHome fails unless path is inside the home directory. Symlinks
// are resolved first, so a link inside home cannot point outside.
func requireUnderHome(path string) error {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}
	if h, err := filepath.EvalSymlinks(home); err == nil {
		home = h
	}
	rel, err := filepath.Rel(home, real)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("quickproxy serves only files inside your home directory %s", home)
	}
	return nil
}

func ctlCreate(path, pattern, target string, overwrite bool, stdout io.Writer) error {
	given := target
	target, err := normalizeTarget(target)
	if err != nil {
		return err
	}
	verb := "created"
	err = modifyConfig(path, func(cfg *Config) error {
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
	if target != given {
		verb += " (" + target + ")"
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
