# Agents

`git-rebind` is a single-package Go CLI. Keep it small. Do not add commands, storage backends, or Git operations that the README does not already describe.

## Layout

- `main.go` parses flags and returns exit codes. `Run` is the test entry point.
- `git.go` finds Git, filters environment overrides, and rejects unsupported layouts.
- `backup.go` copies `.git`, writes manifests, and holds the lock. Backups never live inside the project.
- `rebind.go` implements rebind, `--keep-history`, `--dry-run`, `--undo`, and `--status`.
- `rebind_test.go` builds temporary repositories and runs the CLI in process.

Exit codes are `0` for success, `1` for an operational error, and `2` for usage.

## Invariants

These are not optional:

- Shell out only through `os/exec` argument vectors. Never interpolate a URL into a shell command.
- Never stage, commit, push, fetch, pull, merge, rebase, checkout, reset, or clean.
- Never contact the remote. Do not run `ls-remote`, `fetch`, `clone`, or `pull`.
- Never write working-tree files. The only project paths the tool may create are `.git`, `.git.rebind-old`, and `.git.rebind-current`.
- Copy `.git` to the external backup and verify `HEAD` plus the file count before renaming the live `.git`.
- On failure after that rename, delete the partial replacement and rename `.git.rebind-old` back.
- Refuse bare repositories, a `.git` file, linked worktrees, extra worktrees, `.gitmodules`, and `.git/modules`.
- `--undo` must refuse when the current repository has commits or staged changes unless `--force` was given or the user confirmed on a terminal.
- Do not delete the displaced metadata or the external backup as part of a successful undo.

## Data directory

`GIT_REBIND_DATA_DIR`, otherwise `$XDG_DATA_HOME/git-rebind`, otherwise `~/.local/share/git-rebind`.

Backups are `backups/<sha256 of the absolute project path>/<timestamp>/`. Undo selects the newest manifest with `"state": "available"` for that exact path. Restored backups stay on disk and are not selected again.

## Tests

```bash
make check
```

Tests must use `t.TempDir()`, `t.Chdir`, and a per-test `GIT_REBIND_DATA_DIR`. `TestMain` also points that variable at a throwaway directory so a forgotten override cannot write into the developer's home. Do not run Git against the module root.

A useful test creates a repository, records the worktree (content, mode, and symlink target), runs `Run`, and compares that record. Metadata assertions belong on `git rev-parse`, `git remote get-url`, and the backup copy.

## Changes to avoid

Do not add a command framework, a configuration file, a prune command, network checks, or support for submodules and worktrees. A new safety check is appropriate. A new feature is not, unless the README is updated in the same change and the behavior stays inside the invariants above.
