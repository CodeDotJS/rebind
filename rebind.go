package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cliStdin and cliIsTerm are the confirmation input. Tests replace them so a
// terminal stdin cannot block the suite.
var (
	cliStdin  io.Reader = os.Stdin
	cliIsTerm           = func() bool { return fileIsTerminal(os.Stdin) }
)

func fileIsTerminal(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func confirm(r io.Reader) (bool, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read confirmation: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// rebind never contacts the remote. A nonempty remote must not be merged or adopted.
func rebind(ctx context.Context, stdout io.Writer, url string, keep, dry bool) error {
	wd, err := workingPath()
	if err != nil {
		return err
	}
	repo, err := detect(ctx, wd)
	if errors.Is(err, errNotRepository) {
		return errors.New("not a git repository; run git rebind inside a repository")
	}
	if err != nil {
		return err
	}
	if err := checkLeftovers(repo.Root); err != nil {
		return err
	}
	if dry {
		data, err := dataDir()
		if err != nil {
			return err
		}
		printRebindPlan(stdout, repo, url, data, keep)
		return nil
	}
	return withLock(repo.Root, func(data string) error {
		current, err := detect(ctx, repo.Root)
		if errors.Is(err, errNotRepository) {
			return errors.New("not a git repository; run git rebind inside a repository")
		}
		if err != nil {
			return err
		}
		if err := checkLeftovers(current.Root); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if keep {
			return keepHistory(ctx, stdout, current, url)
		}
		return replaceMetadata(ctx, stdout, current, url, data)
	})
}

func printRebindPlan(w io.Writer, repo Repo, url, data string, keep bool) {
	if keep {
		switch {
		case repo.Origin == url:
			fmt.Fprintf(w, "origin already set to %s\n", url)
		case repo.Origin == "":
			fmt.Fprintf(w, "Would add origin %s\n", url)
		default:
			fmt.Fprintf(w, "Would set origin URL from %s to %s\n", repo.Origin, url)
		}
		fmt.Fprintln(w, "History and working tree would not be changed.")
		fmt.Fprintln(w, "Nothing would be committed or pushed.")
		return
	}
	fmt.Fprintf(w, "Would back up .git under %s\n", filepath.Join(data, "backups"))
	fmt.Fprintln(w, "Would replace Git metadata")
	if repo.Detached {
		fmt.Fprintln(w, "Previous HEAD is detached; the new repository would use Git's default initial branch.")
	}
	fmt.Fprintf(w, "Would set origin to %s\n", url)
	fmt.Fprintln(w, "Working tree files would not be changed.")
	fmt.Fprintln(w, "Nothing would be committed or pushed.")
}

func keepHistory(ctx context.Context, stdout io.Writer, repo Repo, url string) error {
	if repo.Origin == url {
		fmt.Fprintf(stdout, "origin already set to %s\n", url)
		return nil
	}
	if repo.Origin == "" {
		if _, err := runGit(ctx, repo.Root, "remote", "add", "origin", "--", url); err != nil {
			return fmt.Errorf("add origin: %w", err)
		}
		fmt.Fprintf(stdout, "Added origin %s\n", url)
	} else if _, err := runGit(ctx, repo.Root, "remote", "set-url", "origin", "--", url); err != nil {
		return fmt.Errorf("set origin: %w", err)
	} else {
		fmt.Fprintf(stdout, "Set origin to %s\n", url)
	}
	fmt.Fprintln(stdout, "History and working tree were not changed.")
	return nil
}

type swapState struct {
	root      string
	backupDir string
	renamed   bool
}

func (s *swapState) abort(err error) error {
	if err == nil {
		return nil
	}
	if !s.renamed {
		if s.backupDir != "" {
			if rmErr := os.RemoveAll(s.backupDir); rmErr != nil {
				return fmt.Errorf("%w (also could not remove backup %s: %v)", err, s.backupDir, rmErr)
			}
		}
		return err
	}
	if rbErr := rollbackRename(s.root, s.backupDir); rbErr != nil {
		return fmt.Errorf("%v\nrecovery failed: %v", err, rbErr)
	}
	return err
}

func replaceMetadata(ctx context.Context, stdout io.Writer, repo Repo, url, data string) error {
	backup, err := createBackup(data, repo, url)
	if err != nil {
		return err
	}
	swap := &swapState{root: repo.Root, backupDir: backup.Dir}
	gitPath := filepath.Join(repo.Root, ".git")
	oldPath := filepath.Join(repo.Root, rebindOldName)
	if err := os.Rename(gitPath, oldPath); err != nil {
		return swap.abort(fmt.Errorf("move .git aside: %w", err))
	}
	swap.renamed = true
	if err := ctx.Err(); err != nil {
		return swap.abort(err)
	}
	if err := initFresh(ctx, repo, url); err != nil {
		return swap.abort(err)
	}
	if err := os.RemoveAll(oldPath); err != nil {
		return fmt.Errorf("rebind succeeded but could not remove %s: %w", oldPath, err)
	}
	fmt.Fprintf(stdout, "Backed up Git metadata to %s\n", backup.Dir)
	if repo.Detached {
		fmt.Fprintln(stdout, "Previous HEAD was detached; initialized Git's default branch.")
	}
	fmt.Fprintln(stdout, "Reinitialized repository")
	fmt.Fprintf(stdout, "origin %s\n", url)
	fmt.Fprintln(stdout, "No files were changed. Nothing was committed or pushed.")
	return nil
}

func initFresh(ctx context.Context, repo Repo, url string) error {
	args := []string{"init"}
	if !repo.Detached && repo.Branch != "" {
		args = append(args, "-b", repo.Branch)
	}
	if _, err := runGit(ctx, repo.Root, args...); err != nil {
		return fmt.Errorf("initialize repository: %w", err)
	}
	if _, err := runGit(ctx, repo.Root, "remote", "add", "origin", "--", url); err != nil {
		return fmt.Errorf("set origin: %w", err)
	}
	got, err := runGit(ctx, repo.Root, "remote", "get-url", "origin")
	if err != nil {
		return fmt.Errorf("read origin: %w", err)
	}
	if strings.TrimSpace(got) != url {
		return fmt.Errorf("origin is %q after rebind", strings.TrimSpace(got))
	}
	return nil
}

func undo(ctx context.Context, stdout, stderr io.Writer, dry, force bool) error {
	wd, err := workingPath()
	if err != nil {
		return err
	}
	repo, detErr := detect(ctx, wd)
	if detErr != nil && !errors.Is(detErr, errNotRepository) {
		return detErr
	}
	project := wd
	if repo.Root != "" {
		project = repo.Root
	}
	if err := checkLeftovers(project); err != nil {
		return err
	}
	data, err := dataDir()
	if err != nil {
		return err
	}
	chosen, needs, err := undoPlan(ctx, data, project)
	if err != nil {
		return err
	}
	if dry {
		fmt.Fprintf(stdout, "Would restore Git metadata from %s\n", chosen.Dir)
		if needs {
			fmt.Fprintln(stdout, "Confirmation required: yes")
		} else {
			fmt.Fprintln(stdout, "Confirmation required: no")
		}
		fmt.Fprintln(stdout, "Working tree files would not be changed.")
		return nil
	}
	confirmed := false
	if needs && !force {
		if !cliIsTerm() {
			return errors.New("refusing to overwrite changed Git metadata; re-run with --force")
		}
		fmt.Fprintln(stderr, "Current Git metadata has changed since rebind (commits or staged changes exist).")
		fmt.Fprintf(stderr, "Restoring the backup would replace it. A copy would be kept under %s.\n", filepath.Join(data, "displaced", pathKey(project)))
		fmt.Fprint(stderr, "Continue? [y/N]: ")
		ok, err := confirm(cliStdin)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("undo cancelled")
		}
		confirmed = true
	}
	return withLock(project, func(data string) error {
		chosen, needs, err := undoPlan(ctx, data, project)
		if err != nil {
			return err
		}
		if needs && !force && !confirmed {
			return errors.New("refusing to overwrite changed Git metadata; re-run with --force")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		displaced, err := restoreBackup(data, project, chosen)
		if err != nil {
			return err
		}
		chosen.Manifest.State = stateRestored
		now := time.Now().UTC()
		chosen.Manifest.RestoredAt = &now
		if err := writeManifest(chosen.Dir, chosen.Manifest); err != nil {
			return fmt.Errorf("restored Git metadata but could not update the backup manifest: %w", err)
		}
		fmt.Fprintf(stdout, "Restored Git metadata from %s\n", chosen.Dir)
		if displaced != "" {
			fmt.Fprintf(stdout, "Displaced current metadata to %s\n", displaced)
		}
		fmt.Fprintln(stdout, "No files were changed. Nothing was committed or pushed.")
		return nil
	})
}

func undoPlan(ctx context.Context, data, project string) (storedBackup, bool, error) {
	items, err := listBackups(data, project)
	if err != nil {
		return storedBackup{}, false, err
	}
	chosen := latestAvailable(items)
	if chosen == nil {
		return storedBackup{}, false, errors.New("nothing to undo for this project")
	}
	item := *chosen
	if _, err := os.Stat(filepath.Join(item.GitDir, "HEAD")); err != nil {
		return storedBackup{}, false, fmt.Errorf("backup %s is incomplete", item.Dir)
	}
	repo, detErr := detect(ctx, project)
	if detErr != nil && !errors.Is(detErr, errNotRepository) {
		return storedBackup{}, false, detErr
	}
	gitPath := filepath.Join(project, ".git")
	info, statErr := os.Lstat(gitPath)
	hasGit := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return storedBackup{}, false, fmt.Errorf("inspect .git: %w", statErr)
	}
	if hasGit && !info.IsDir() {
		return storedBackup{}, false, errors.New(".git is not a directory (linked worktrees and submodules are not supported)")
	}
	needs := false
	if detErr == nil {
		needs = repo.changed()
	} else if hasGit {
		needs = true
	}
	return item, needs, nil
}

func restoreBackup(data, project string, chosen storedBackup) (string, error) {
	gitPath := filepath.Join(project, ".git")
	hold := filepath.Join(project, rebindHoldName)
	hadGit := false
	if _, err := os.Lstat(gitPath); err == nil {
		hadGit = true
		if err := os.Rename(gitPath, hold); err != nil {
			return "", fmt.Errorf("move .git aside: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect .git: %w", err)
	}

	rollback := func(err error) error {
		if rmErr := os.RemoveAll(gitPath); rmErr != nil {
			return fmt.Errorf("%w\nrecovery failed: %v", err, rmErr)
		}
		if !hadGit {
			return err
		}
		if rbErr := os.Rename(hold, gitPath); rbErr != nil {
			return fmt.Errorf("%w\nrecovery failed: %v\nleftover: %s\nbackup: %s", err, rbErr, hold, chosen.Dir)
		}
		return err
	}

	if err := copyTree(chosen.GitDir, gitPath); err != nil {
		return "", rollback(fmt.Errorf("restore Git metadata: %w", err))
	}
	if err := verifyCopy(chosen.GitDir, gitPath); err != nil {
		return "", rollback(err)
	}

	if !hadGit {
		return "", nil
	}
	displaced, err := displaceGit(data, project, hold)
	if err != nil {
		if _, statErr := os.Lstat(hold); statErr == nil && displaced == "" {
			return "", rollback(err)
		}
		return displaced, fmt.Errorf("restored Git metadata but could not finish preserving the previous metadata: %w", err)
	}
	return displaced, nil
}

func status(ctx context.Context, stdout io.Writer) error {
	wd, err := workingPath()
	if err != nil {
		return err
	}
	repo, detErr := detect(ctx, wd)
	if detErr != nil && !errors.Is(detErr, errNotRepository) && !isLayout(detErr) {
		return detErr
	}
	project := wd
	if repo.Root != "" {
		project = repo.Root
	}
	writeRepoStatus(stdout, project, repo, detErr)
	if err := mentionLeftovers(stdout, project); err != nil {
		return err
	}
	fmt.Fprintln(stdout)
	return writeBackups(stdout, project)
}

func writeRepoStatus(w io.Writer, project string, repo Repo, detErr error) {
	fmt.Fprintf(w, "Repository: %s\n", project)
	if errors.Is(detErr, errNotRepository) {
		fmt.Fprintln(w, "Not a Git repository")
		return
	}
	if isLayout(detErr) {
		fmt.Fprintf(w, "Unsupported: %s\n", detErr.Error())
	}
	if detErr != nil && repo.Branch == "" && repo.Origin == "" && repo.Head == "" && !repo.Detached {
		return
	}
	if repo.Detached {
		fmt.Fprintln(w, "Branch: detached HEAD")
	} else if repo.Branch != "" {
		fmt.Fprintf(w, "Branch: %s\n", repo.Branch)
	}
	if repo.Origin == "" {
		fmt.Fprintln(w, "Origin: (none)")
	} else {
		fmt.Fprintf(w, "Origin: %s\n", repo.Origin)
	}
	if repo.Head == "" {
		fmt.Fprintln(w, "HEAD: (no commits)")
	} else {
		fmt.Fprintf(w, "HEAD: %s\n", repo.Head)
	}
}

func mentionLeftovers(w io.Writer, root string) error {
	for _, name := range []string{rebindOldName, rebindHoldName} {
		path := filepath.Join(root, name)
		if _, err := os.Lstat(path); err == nil {
			fmt.Fprintf(w, "Leftover: %s\n", path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect %s: %w", path, err)
		}
	}
	return nil
}

func writeBackups(w io.Writer, project string) error {
	data, err := dataDir()
	if err != nil {
		return err
	}
	items, err := listBackups(data, project)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "Backups:")
	if len(items) == 0 {
		fmt.Fprintln(w, "  (none)")
		return nil
	}
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		origin := item.Manifest.OldOrigin
		if origin == "" {
			origin = "(none)"
		}
		branch := item.Manifest.OldBranch
		if item.Manifest.Detached {
			branch = "detached"
		} else if branch == "" {
			branch = "(unknown)"
		}
		fmt.Fprintf(w, "  %s  %s  origin=%s  branch=%s\n",
			item.Manifest.CreatedAt.UTC().Format(time.RFC3339),
			item.Manifest.State,
			origin,
			branch,
		)
	}
	return nil
}
