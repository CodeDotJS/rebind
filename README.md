# git-rebind

`git-rebind` points an existing local Git checkout at a different repository without changing the files in the working tree. The binary is named `git-rebind`, so Git invokes it as `git rebind` once it is on `PATH`.

It does not stage, commit, push, fetch, merge, or checkout. It also does not look at the remote, so a remote that already has commits is never merged or adopted.

## Install

From a clone of this repository:

```bash
go install .
```

Or build it locally:

```bash
make build
install bin/git-rebind "$(go env GOPATH)/bin"
```

`$(go env GOPATH)/bin` must be on `PATH`. Confirm with `git rebind --version`.

Git 2.28 or newer is required.

## Usage

```text
git rebind <url> [--keep-history] [--dry-run]
git rebind --undo [--dry-run] [--force]
git rebind --status
git rebind --version
```

| Command | Effect |
| --- | --- |
| `git rebind <url>` | Copy `.git` to a backup outside the project, create fresh Git metadata, and set `origin` to `<url>`. |
| `git rebind <url> --keep-history` | Change `origin` only. Other remotes and all history stay. No backup is created. |
| `--dry-run` | Print the plan. Nothing is written. |
| `git rebind --undo` | Restore the newest unrestored backup for this directory. |
| `git rebind --undo --force` | Restore even when the current metadata has commits or staged changes. The metadata being replaced is kept outside the project. |
| `git rebind --status` | Show the repository path, branch, `HEAD`, `origin`, and backups. |

A URL must not be empty, start with `-`, or contain control characters. It is passed to Git as one argument.

## Examples

Replace history with a fresh repository that points at a new remote. The old metadata is backed up first. Project files stay where they are, including edits, untracked files, and ignored files.

```bash
git rebind git@github.com:me/new-repo.git
```

Preview the same operation:

```bash
git rebind --dry-run git@github.com:me/new-repo.git
```

Keep the current commits and only retarget `origin`:

```bash
git rebind --keep-history git@github.com:me/new-repo.git
```

See the result, then restore the previous metadata:

```bash
git rebind --status
git rebind --undo
```

If you committed or staged files after a full rebind, undo stops unless you confirm at a terminal or pass `--force`:

```bash
git rebind --undo --force
```

Nothing is pushed. If the remote already has commits, a later `git push` may be rejected. `git-rebind` will not force-push for you.

## Backups

Backups are stored outside the project:

1. `GIT_REBIND_DATA_DIR`, if set
2. `$XDG_DATA_HOME/git-rebind`
3. `~/.local/share/git-rebind`

Each full rebind writes `backups/<project-hash>/<timestamp>/manifest.json` and a complete copy of `.git`. The hash is only a directory name. The manifest records the absolute project path, so moving or renaming the project disconnects it from its backups.

`--undo` restores the newest backup whose manifest is still `available`, then marks it `restored`. An older available backup can be restored by another undo. A backup that is already restored is not used again.

The metadata that undo replaces is copied to `displaced/<project-hash>/<timestamp>/git` before it is removed from the project. Undo does not delete the external backup.

`--keep-history` does not create a backup. `--undo` does not revert a URL-only change.

While a rebind or undo is running, a lock file is held at `locks/<project-hash>.lock`. If a crash leaves that file behind and no `git-rebind` process is running, delete the lock file and run `--status`.

## Limitations

- Only a normal repository with one worktree is supported. Bare repositories, linked worktrees, extra worktrees, and submodules are refused.
- The remote is never contacted. `git-rebind` cannot tell whether the remote already has commits.
- A full rebind creates a new repository. Local config, hooks, and the reflog stay in the backup; they are not copied into the fresh metadata. The current branch name is kept unless `HEAD` is detached.
- Backups are keyed by the absolute project path.
- Run `--undo` from the project root if `.git` has already been removed. From a subdirectory, undo works only while Git can still identify the worktree.

## Recovery

Project files outside `.git` are never part of the backup. Do not delete them to recover metadata.

If a rebind is interrupted, look for `.git.rebind-old` in the project:

- If `.git` is missing or only partly created, remove that partial `.git` and rename `.git.rebind-old` to `.git`.
- If the new `.git` is already valid and you want it, remove `.git.rebind-old`. The external backup is still available for `--undo`.
- If both directories exist, do not delete either until you know which metadata you want. `--status` reports leftover directories.

If an undo is interrupted, `.git.rebind-current` is the metadata that was moved aside. If `.git` is missing, rename `.git.rebind-current` to `.git`.

You can also restore a backup by hand. Each backup's `git` directory is a complete `.git` tree. Replace the project's `.git` with a copy of that directory only after you have saved the current `.git` somewhere outside the project.

## Development

Run the full local check with:

```bash
make check
```

See [CONTRIBUTING.md](CONTRIBUTING.md) and [AGENTS.md](AGENTS.md) for the safety rules and test setup.
