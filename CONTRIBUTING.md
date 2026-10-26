# Contributing

Thanks for helping with git-rebind. The useful change is usually a bug fix, a clearer error, or a test that locks down an existing safety rule.

## Setup

Install Go 1.25 or newer and Git 2.28 or newer. From a clone:

```bash
make check
```

`make check` confirms formatting, runs `go vet`, runs the tests, and builds `bin/git-rebind`. `make fmt` rewrites Go files with `gofmt`.

The program stays in one package and uses only the Go standard library. A new dependency needs a reason that the standard library cannot cover.

## Safety

git-rebind replaces or restores Git metadata. It does not change project files, and it does not talk to remotes.

- Call Git with `os/exec` arguments. Do not build a shell command.
- Do not stage, commit, push, fetch, merge, checkout, or reset.
- Keep backups outside the project, under the data directory described in the README.
- Refuse layouts the tool does not understand: bare repositories, linked worktrees, extra worktrees, and submodules.
- If a change can destroy commits or staged changes during undo, require `--force` or a terminal confirmation.

## Tests

Add or adjust a test in `rebind_test.go` for the behavior you change. Use a temporary repository and set `GIT_REBIND_DATA_DIR` to a temporary directory. Tests must not read or write the checkout you are developing.

Run the specific test while iterating:

```bash
go test -timeout 120s -run TestName -count=1
```

Before opening a pull request, run `make check`.

## Pull requests

Describe the user-visible behavior and the safety consequence. Keep the diff limited to the problem you are solving. Update the README when a command, flag, backup location, or limitation changes.
