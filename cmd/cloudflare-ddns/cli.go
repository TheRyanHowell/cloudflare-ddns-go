package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/spf13/cobra"
)

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2

	programName = "cloudflare-ddns"
)

// app holds what the commands run against: the environment, the output
// streams and the logger.
type app struct {
	getenv         func(string) string
	stdout, stderr io.Writer
	logger         *slog.Logger
	// httpClient carries the IP lookups and Cloudflare requests. Nil selects
	// each package's own client.
	httpClient *http.Client
}

// usageError is a wrong command line: an unknown command, a bad flag or a
// stray argument. cmd is the command whose usage helps.
type usageError struct {
	err error
	cmd *cobra.Command
}

func (e *usageError) Error() string { return e.err.Error() }

func (e *usageError) Unwrap() error { return e.err }

// execute runs the command in args and returns the process exit code. Runtime
// errors are logged and usage errors print the command's usage.
func (a app) execute(ctx context.Context, args []string) int {
	root := a.newRootCmd()
	// A nil slice would make cobra read os.Args.
	root.SetArgs(append([]string{}, args...))

	err := root.ExecuteContext(ctx)
	var usage *usageError
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &usage):
		printf(a.stderr, "Error: %v\n\n%s", usage, usage.cmd.UsageString())
		return exitUsage
	}
	a.logger.Error("exiting", "error", err)
	return exitFailure
}

// newRootCmd builds the command tree. The root runs the daemon, so a bare
// invocation behaves as it did before there were subcommands.
func (a app) newRootCmd() *cobra.Command {
	root := a.newCommand(programName, "Keep a Cloudflare A record pointed at this host's public IP address", settingNames(), a.runDaemon)
	root.SilenceUsage = true
	root.SilenceErrors = true
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error { return &usageError{err: err, cmd: cmd} })
	root.AddCommand(
		a.newCommand("run", "Keep the record in sync until stopped (the default command)", settingNames(), a.runDaemon),
		a.newCommand(healthcheckCommand, "Probe the running daemon's health endpoint", []string{"health-addr"}, a.runHealthcheck),
		a.newCommand("ip", "Print the public IP address", []string{"providers"}, a.runIP),
		a.newCommand("version", "Print the version", nil, a.runVersion),
	)
	return root
}

// newCommand builds a command that takes no arguments and the named flags, and
// calls run with the environment those flags overlay.
func (a app) newCommand(use, short string, flags []string, run func(ctx context.Context, getenv func(string) string) error) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd.Context(), overlay(a.getenv, cmd.Flags()))
		},
	}
	addFlags(cmd, flags...)
	return cmd
}

// noArgs rejects positional arguments; on the root they name an unknown command.
func noArgs(cmd *cobra.Command, args []string) error {
	switch {
	case len(args) == 0:
		return nil
	case cmd.HasSubCommands():
		return &usageError{err: fmt.Errorf("unknown command %q", args[0]), cmd: cmd}
	}
	return &usageError{err: fmt.Errorf("unexpected argument %q", args[0]), cmd: cmd}
}

// printf writes to a user's terminal, where a failed write has nowhere to be
// reported.
func printf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
