package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	stateAvailable = "available"
	stateRestored  = "restored"

	rebindOldName  = ".git.rebind-old"
	rebindHoldName = ".git.rebind-current"
)

type manifest struct {
	ProjectPath string     `json:"project_path"`
	CreatedAt   time.Time  `json:"created_at"`
	OldBranch   string     `json:"old_branch"`
	Detached    bool       `json:"detached"`
	OldOrigin   string     `json:"old_origin"`
	NewOrigin   string     `json:"new_origin"`
	OldHEAD     string     `json:"old_head"`
	State       string     `json:"state"`
	RestoredAt  *time.Time `json:"restored_at,omitempty"`
}

type storedBackup struct {
	ID       string
	Dir      string
	GitDir   string
	Manifest manifest
}

func dataDir() (string, error) {
	var dir string
	switch {
	case os.Getenv("GIT_REBIND_DATA_DIR") != "":
		dir = os.Getenv("GIT_REBIND_DATA_DIR")
	case os.Getenv("XDG_DATA_HOME") != "":
		dir = filepath.Join(os.Getenv("XDG_DATA_HOME"), "git-rebind")
	default:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find a backup directory: %w", err)
		}
		dir = filepath.Join(home, ".local", "share", "git-rebind")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}

func pathKey(project string) string {
	sum := sha256.Sum256([]byte(project))
	return hex.EncodeToString(sum[:])
}

func acquireLock(data, project string) (func(), error) {
	locks := filepath.Join(data, "locks")
	if err := os.MkdirAll(locks, 0o755); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	path := filepath.Join(locks, pathKey(project)+".lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("another git-rebind may be running; if not, remove %s", path)
		}
		return nil, fmt.Errorf("create lock %s: %w", path, err)
	}
	_, writeErr := fmt.Fprintf(file, "%d\n", os.Getpid())
	closeErr := file.Close()
	if writeErr != nil {
		os.Remove(path)
		return nil, writeErr
	}
	if closeErr != nil {
		os.Remove(path)
		return nil, closeErr
	}
	return func() { os.Remove(path) }, nil
}

func withLock(project string, fn func(data string) error) error {
	data, err := dataDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(data, 0o755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	unlock, err := acquireLock(data, project)
	if err != nil {
		return err
	}
	defer unlock()
	return fn(data)
}

func createBackup(data string, repo Repo, newOrigin string) (storedBackup, error) {
	id := time.Now().UTC().Format("20060102T150405.000000000Z")
	dir := filepath.Join(data, "backups", pathKey(repo.Root), id)
	gitDst := filepath.Join(dir, "git")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return storedBackup{}, fmt.Errorf("create backup directory: %w", err)
	}
	cleanup := func(err error) (storedBackup, error) {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			return storedBackup{}, fmt.Errorf("%w (also could not remove %s: %v)", err, dir, rmErr)
		}
		return storedBackup{}, err
	}
	if err := copyTree(repo.GitDir, gitDst); err != nil {
		return cleanup(fmt.Errorf("back up .git: %w", err))
	}
	if err := verifyCopy(repo.GitDir, gitDst); err != nil {
		return cleanup(err)
	}
	item := storedBackup{
		ID:     id,
		Dir:    dir,
		GitDir: gitDst,
		Manifest: manifest{
			ProjectPath: repo.Root,
			CreatedAt:   time.Now().UTC(),
			OldBranch:   repo.Branch,
			Detached:    repo.Detached,
			OldOrigin:   repo.Origin,
			NewOrigin:   newOrigin,
			OldHEAD:     repo.Head,
			State:       stateAvailable,
		},
	}
	if err := writeManifest(dir, item.Manifest); err != nil {
		return cleanup(err)
	}
	return item, nil
}

func listBackups(data, project string) ([]storedBackup, error) {
	base := filepath.Join(data, "backups", pathKey(project))
	entries, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read backups: %w", err)
	}
	var items []storedBackup
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(base, entry.Name())
		item, err := readBackup(dir, entry.Name())
		if err != nil {
			return nil, err
		}
		if item.Manifest.ProjectPath != project {
			continue
		}
		items = append(items, item)
	}
	sortBackups(items)
	return items, nil
}

func sortBackups(items []storedBackup) {
	for i := 1; i < len(items); i++ {
		item := items[i]
		j := i
		for j > 0 && items[j-1].Manifest.CreatedAt.After(item.Manifest.CreatedAt) {
			items[j] = items[j-1]
			j--
		}
		items[j] = item
	}
}

func latestAvailable(items []storedBackup) *storedBackup {
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Manifest.State == stateAvailable {
			return &items[i]
		}
	}
	return nil
}

func readBackup(dir, id string) (storedBackup, error) {
	manifest, err := readManifest(dir)
	if err != nil {
		return storedBackup{}, err
	}
	return storedBackup{
		ID:       id,
		Dir:      dir,
		GitDir:   filepath.Join(dir, "git"),
		Manifest: manifest,
	}, nil
}

func readManifest(dir string) (manifest, error) {
	body, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return manifest{}, fmt.Errorf("read manifest in %s: %w", dir, err)
	}
	var item manifest
	if err := json.Unmarshal(body, &item); err != nil {
		return manifest{}, fmt.Errorf("manifest in %s is invalid: %w", dir, err)
	}
	if item.ProjectPath == "" || item.CreatedAt.IsZero() {
		return manifest{}, fmt.Errorf("manifest in %s is missing its project path or time", dir)
	}
	if item.State != stateAvailable && item.State != stateRestored {
		return manifest{}, fmt.Errorf("manifest in %s has unknown state %q", dir, item.State)
	}
	return item, nil
}

func writeManifest(dir string, item manifest) error {
	body, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	body = append(body, '\n')
	tmp := filepath.Join(dir, "manifest.json.tmp")
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "manifest.json")); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

func verifyCopy(src, dst string) error {
	sourceHEAD, err := os.ReadFile(filepath.Join(src, "HEAD"))
	if err != nil {
		return fmt.Errorf("backup source is missing HEAD: %w", err)
	}
	copiedHEAD, err := os.ReadFile(filepath.Join(dst, "HEAD"))
	if err != nil {
		return fmt.Errorf("backup is missing HEAD: %w", err)
	}
	if string(sourceHEAD) != string(copiedHEAD) {
		return errors.New("backup HEAD does not match the repository")
	}
	sourceCount, err := countEntries(src)
	if err != nil {
		return fmt.Errorf("count .git files: %w", err)
	}
	copiedCount, err := countEntries(dst)
	if err != nil {
		return fmt.Errorf("count backup files: %w", err)
	}
	if sourceCount != copiedCount {
		return fmt.Errorf("backup file count %d does not match source %d", copiedCount, sourceCount)
	}
	return nil
}

func countEntries(root string) (int, error) {
	count := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root || entry.IsDir() {
			return nil
		}
		count++
		return nil
	})
	return count, err
}

func copyTree(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
	}
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == src {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			return copyLink(dst, path, target, rel)
		case info.IsDir():
			return os.Mkdir(target, info.Mode().Perm())
		case info.Mode().IsRegular():
			return copyFile(path, target, info.Mode().Perm())
		default:
			return fmt.Errorf("unsupported file in .git: %s", rel)
		}
	})
}

func copyLink(root, src, target, rel string) error {
	link, err := os.Readlink(src)
	if err != nil {
		return err
	}
	if filepath.IsAbs(link) {
		return fmt.Errorf("refusing to copy absolute symlink %s", rel)
	}
	resolved := filepath.Join(filepath.Dir(target), link)
	relToRoot, err := filepath.Rel(root, resolved)
	if err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("refusing to copy symlink %s: it points outside the tree", rel)
	}
	return os.Symlink(link, target)
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(dst, mode.Perm())
}

func displaceGit(data, project, src string) (string, error) {
	id := time.Now().UTC().Format("20060102T150405.000000000Z")
	dst := filepath.Join(data, "displaced", pathKey(project), id, "git")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("create displaced metadata directory: %w", err)
	}
	if err := copyTree(src, dst); err != nil {
		os.RemoveAll(filepath.Dir(dst))
		return "", fmt.Errorf("preserve current Git metadata: %w", err)
	}
	if err := verifyCopy(src, dst); err != nil {
		os.RemoveAll(filepath.Dir(dst))
		return "", err
	}
	if err := os.RemoveAll(src); err != nil {
		return dst, fmt.Errorf("copied current Git metadata to %s but could not remove %s: %w", dst, src, err)
	}
	return dst, nil
}

func checkLeftovers(root string) error {
	for _, name := range []string{rebindOldName, rebindHoldName} {
		path := filepath.Join(root, name)
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("found leftover %s from an interrupted run; if .git is incomplete, replace it with this directory, otherwise remove the leftover", path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect %s: %w", path, err)
		}
	}
	return nil
}

func rollbackRename(root, backupDir string) error {
	gitPath := filepath.Join(root, ".git")
	old := filepath.Join(root, rebindOldName)
	if _, err := os.Lstat(gitPath); err == nil {
		if err := os.RemoveAll(gitPath); err != nil {
			return fmt.Errorf("remove partial .git: %w\nbackup: %s\nleftover: %s", err, backupDir, old)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect .git: %w\nbackup: %s\nleftover: %s", err, backupDir, old)
	}
	if err := os.Rename(old, gitPath); err != nil {
		return fmt.Errorf("restore %s: %w\nbackup: %s", old, err, backupDir)
	}
	if err := os.RemoveAll(backupDir); err != nil {
		return fmt.Errorf("restored .git but could not remove backup %s: %w", backupDir, err)
	}
	return nil
}
