package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
)

// version is set by -ldflags "-X main.version=...".
var version = "dev"

const usageText = `git-rebind — point an existing checkout at a different Git repository

Usage:
  git rebind <url> [--keep-history] [--dry-run]
  git rebind --undo [--dry-run] [--force]
  git rebind --status
  git rebind --version

git-rebind is invoked as "git rebind" when the binary is on PATH.

Options:
  --keep-history   Change origin and keep the current Git history
  --dry-run        Print the plan without writing
  --undo           Restore Git metadata from the newest backup
  --force          Allow --undo to replace commits or staged changes
  --status         Show the repository, origin, and backups
  --version        Print the version

The command never stages, commits, pushes, fetches, or edits project files.
Backups are stored outside the project. See the README for recovery.
`

type usageError struct {
	msg string
}

func (e *usageError) Error() string { return e.msg }

type cliOptions struct {
	keepHistory bool
	dryRun      bool
	undo        bool
	status      bool
	showVersion bool
	force       bool
	url         string
	done        bool
	code        int
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

// Run executes one git-rebind invocation and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return run(ctx, args, stdout, stderr)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, err := parseArgs(args, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "git-rebind: %s\n", err)
		var usage *usageError
		if errors.As(err, &usage) {
			return 2
		}
		return 1
	}
	if opts.done {
		return opts.code
	}
	if opts.showVersion {
		fmt.Fprintf(stdout, "git-rebind %s\n", version)
		return 0
	}

	var opErr error
	switch {
	case opts.status:
		opErr = status(ctx, stdout)
	case opts.undo:
		opErr = undo(ctx, stdout, stderr, opts.dryRun, opts.force)
	default:
		opErr = rebind(ctx, stdout, opts.url, opts.keepHistory, opts.dryRun)
	}
	if opErr != nil {
		fmt.Fprintf(stderr, "git-rebind: %s\n", opErr)
		var usage *usageError
		if errors.As(opErr, &usage) {
			return 2
		}
		return 1
	}
	return 0
}

func parseArgs(args []string, stdout, stderr io.Writer) (cliOptions, error) {
	fs := flag.NewFlagSet("git-rebind", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var opts cliOptions
	fs.BoolVar(&opts.keepHistory, "keep-history", false, "Change origin and keep the current Git history")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "Print the plan without writing")
	fs.BoolVar(&opts.undo, "undo", false, "Restore Git metadata from the newest backup")
	fs.BoolVar(&opts.status, "status", false, "Show the repository, origin, and backups")
	fs.BoolVar(&opts.showVersion, "version", false, "Print the version")
	fs.BoolVar(&opts.force, "force", false, "Allow --undo to replace commits or staged changes")
	fs.Usage = func() {}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usageText)
			opts.done = true
			return opts, nil
		}
		opts.done = true
		opts.code = 2
		return opts, nil
	}

	rest := fs.Args()
	if opts.showVersion {
		if opts.keepHistory || opts.dryRun || opts.undo || opts.status || opts.force || len(rest) > 0 {
			return opts, &usageError{msg: "--version cannot be combined with other arguments"}
		}
		return opts, nil
	}
	if opts.status && opts.undo {
		return opts, &usageError{msg: "--status and --undo cannot be combined"}
	}
	if opts.status {
		if opts.keepHistory || opts.dryRun || opts.force || len(rest) > 0 {
			return opts, &usageError{msg: "--status does not take a URL or other operation flags"}
		}
		return opts, nil
	}
	if opts.undo {
		if opts.keepHistory || len(rest) > 0 {
			return opts, &usageError{msg: "--undo does not take a URL or --keep-history"}
		}
		return opts, nil
	}
	if opts.force {
		return opts, &usageError{msg: "--force is only valid with --undo"}
	}
	if len(rest) != 1 {
		if opts.keepHistory {
			return opts, &usageError{msg: "--keep-history requires a remote URL"}
		}
		return opts, &usageError{msg: "exactly one remote URL is required"}
	}
	opts.url = rest[0]
	if err := validateURL(opts.url); err != nil {
		return opts, &usageError{msg: err.Error()}
	}
	return opts, nil
}

func validateURL(url string) error {
	if url == "" || strings.TrimSpace(url) != url {
		return errors.New("remote URL is empty or surrounded by whitespace")
	}
	if strings.HasPrefix(url, "-") {
		return errors.New("remote URL must not start with '-'")
	}
	for _, r := range url {
		if r < 0x20 || r == 0x7f {
			return errors.New("remote URL contains a control character")
		}
	}
	return nil
}
