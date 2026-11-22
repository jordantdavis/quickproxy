package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// configFlagUsage is the help text of the --config flag, which the server and
// the route subcommands share.
const configFlagUsage = "path to the JSON route file (default $XDG_CONFIG_HOME/quickproxy/routes.json or ~/.config/quickproxy/routes.json)"

// exitError carries a process exit code out of a command. If err is nil, run
// prints nothing, for example when the server already logged the error.
type exitError struct {
	code  int
	err   error
	usage bool // print a hint to run --help
}

func (e *exitError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit code %d", e.code)
	}
	return e.err.Error()
}

func (e *exitError) Unwrap() error { return e.err }

// usageError is a command-line mistake. run exits 2 for it.
func usageError(format string, args ...any) error {
	return &exitError{code: 2, err: fmt.Errorf(format, args...), usage: true}
}

// run executes the command line args (without the program name) and returns
// the process exit code: 0 on success, 2 on a usage error, 1 on another
// error, or the code in an exitError.
func run(args []string, stdout, stderr io.Writer) int {
	root := newRootCmd(stdout, stderr)
	root.SetArgs(normalizeArgs(root, args))
	// Cobra parses the flags and checks the arguments before it calls
	// PersistentPreRun. An error before that point is a usage error.
	started := false
	root.PersistentPreRun = func(*cobra.Command, []string) { started = true }
	cmd, err := root.ExecuteC()
	if err == nil {
		return 0
	}
	code, usage := 2, true
	if started {
		code, usage = 1, false
	}
	var ee *exitError
	if errors.As(err, &ee) {
		code, usage, err = ee.code, ee.usage, ee.err
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
	}
	if usage && err != nil {
		fmt.Fprintf(stderr, "Run '%s --help' for usage.\n", cmd.CommandPath())
	}
	return code
}

func newRootCmd(stdout, stderr io.Writer) *cobra.Command {
	var configPath, listen string
	var showVersion bool
	root := &cobra.Command{
		Use:   "quickproxy",
		Short: "Send each request on one local port to a local app by path prefix",
		Long: `quickproxy listens on one loopback port and sends each request to a
local app by path prefix. To reach the port from other machines, put a
separate tool in front of it, for example "tailscale serve 8080".

Without a subcommand, quickproxy runs the proxy server.`,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if showVersion {
				fmt.Fprintln(cmd.OutOrStdout(), versionString())
				return nil
			}
			return serve(configPath, listen, stderr)
		},
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	f := root.Flags()
	f.StringVar(&configPath, "config", "", configFlagUsage)
	_ = root.MarkFlagFilename("config", "json")
	f.StringVar(&listen, "listen", "", `listen address (overrides "listen" in the file; default 127.0.0.1:8080)`)
	f.BoolVar(&showVersion, "version", false, "print the version and exit")

	root.AddCommand(
		newUpdateCmd(stdout),
		newGetCmd(stdout, stderr),
		newCreateCmd(stdout),
		newDeleteCmd(stdout),
		newPrimeCmd(stdout),
		newInitCmd(stdout),
	)
	return root
}

// normalizeArgs turns the single-dash long flags of older releases, such as
// -config or -version, into the double-dash form that Cobra expects. It
// changes only names that some command defines, and nothing after "--".
// Older releases of "quickproxy update" and install.sh run "-version", so
// this form must keep working.
func normalizeArgs(root *cobra.Command, args []string) []string {
	long := map[string]bool{"help": true}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		add := func(f *pflag.Flag) { long[f.Name] = true }
		c.Flags().VisitAll(add)
		c.PersistentFlags().VisitAll(add)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)

	out := make([]string, len(args))
	for i, a := range args {
		if a == "--" {
			copy(out[i:], args[i:])
			break
		}
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") {
			name, _, _ := strings.Cut(a[1:], "=")
			if len(name) > 1 && long[name] {
				a = "-" + a
			}
		}
		out[i] = a
	}
	return out
}
