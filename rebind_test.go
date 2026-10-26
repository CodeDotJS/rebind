package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var moduleRoot string

func TestMain(m *testing.M) {
	for _, key := range []string{
		"GIT_DIR",
		"GIT_WORK_TREE",
		"GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY",
		"GIT_COMMON_DIR",
		"GIT_NAMESPACE",
	} {
		os.Unsetenv(key)
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	moduleRoot = wd
	data, err := os.MkdirTemp("", "git-rebind-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cfg, err := os.CreateTemp("", "git-rebind-gitconfig-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cfg.Close()
	os.Setenv("GIT_REBIND_DATA_DIR", data)
	os.Setenv("GIT_CONFIG_GLOBAL", cfg.Name())
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Setenv("GIT_AUTHOR_NAME", "git-rebind test")
	os.Setenv("GIT_AUTHOR_EMAIL", "git-rebind@example.com")
	os.Setenv("GIT_COMMITTER_NAME", "git-rebind test")
	os.Setenv("GIT_COMMITTER_EMAIL", "git-rebind@example.com")
	code := m.Run()
	os.RemoveAll(data)
	os.Remove(cfg.Name())
	os.Exit(code)
}

func testEnv(t *testing.T) string {
	t.Helper()
	data := t.TempDir()
	t.Setenv("GIT_REBIND_DATA_DIR", data)
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return data
}

func chdirTemp(t *testing.T, dir string) {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := filepath.EvalSymlinks(abs)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	if got == root {
		t.Fatalf("refusing to run in the module root %s", root)
	}
	t.Chdir(abs)
}

func gitOut(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitOut(t, dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func mustGitDir(t *testing.T, gitDir string, args ...string) string {
	t.Helper()
	full := append([]string{"--git-dir", gitDir}, args...)
	return mustGit(t, filepath.Dir(gitDir), full...)
}

func runCLI(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "-b", "main")
	mustGit(t, dir, "remote", "add", "origin", "git@github.com:old/repo.git")
	writeFile(t, filepath.Join(dir, "tracked.txt"), "tracked\n", 0o644)
	mustGit(t, dir, "add", "tracked.txt")
	mustGit(t, dir, "commit", "-m", "initial")
	return dir
}

func makeRepo(t *testing.T) string {
	t.Helper()
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, ".gitignore"), "ignored.txt\n", 0o644)
	mustGit(t, dir, "add", ".gitignore")
	mustGit(t, dir, "commit", "-m", "ignore")
	writeFile(t, filepath.Join(dir, "tracked.txt"), "tracked\nmodified\n", 0o644)
	writeFile(t, filepath.Join(dir, "untracked.txt"), "untracked\n", 0o600)
	writeFile(t, filepath.Join(dir, "ignored.txt"), "ignored\n", 0o644)
	if err := os.Symlink("tracked.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

type fileSnap struct {
	mode os.FileMode
	body string
}

func snapshot(t *testing.T, root string) map[string]fileSnap {
	t.Helper()
	out := map[string]fileSnap{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(os.PathSeparator)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			out[rel] = fileSnap{mode: info.Mode(), body: "link:" + target}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = fileSnap{mode: info.Mode(), body: string(body)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func snapsEqual(t *testing.T, before, after map[string]fileSnap) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("file count %d != %d\nbefore %v\nafter %v", len(before), len(after), snapKeys(before), snapKeys(after))
	}
	for name, got := range after {
		want, ok := before[name]
		if !ok || got != want {
			t.Fatalf("file %s changed\nbefore %#v\nafter %#v", name, want, got)
		}
	}
}

func snapKeys(files map[string]fileSnap) []string {
	keys := make([]string, 0, len(files))
	for name := range files {
		keys = append(keys, name)
	}
	return keys
}

func backupDirs(t *testing.T, data string) []string {
	t.Helper()
	root := filepath.Join(data, "backups")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	}
	var dirs []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == "manifest.json" {
			dirs = append(dirs, filepath.Dir(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return dirs
}

func displacedGitDirs(t *testing.T, data string) []string {
	t.Helper()
	root := filepath.Join(data, "displaced")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	}
	var dirs []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == "git" {
			if _, err := os.Stat(filepath.Join(path, "HEAD")); err == nil {
				dirs = append(dirs, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return dirs
}

func backedUpPath(stdout string) string {
	const prefix = "Backed up Git metadata to "
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}

func assertOutside(t *testing.T, project, path string) {
	t.Helper()
	rel, err := filepath.Rel(project, path)
	if err != nil {
		t.Fatal(err)
	}
	if rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		t.Fatalf("path %s is inside %s", path, project)
	}
}

func resolved(t *testing.T, path string) string {
	t.Helper()
	got, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func prependGitStub(t *testing.T, script string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	body := strings.ReplaceAll(script, "{{GIT}}", strconv.Quote(real))
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestRebindReplacesMetadataAndPreservesFiles(t *testing.T) {
	data := testEnv(t)
	repo := makeRepo(t)
	chdirTemp(t, repo)
	head := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))
	before := snapshot(t, repo)
	for _, name := range []string{"tracked.txt", "untracked.txt", "ignored.txt", "link.txt", ".gitignore"} {
		if _, ok := before[name]; !ok {
			t.Fatalf("fixture missing %s", name)
		}
	}
	if before["tracked.txt"].body != "tracked\nmodified\n" {
		t.Fatalf("tracked content %#v", before["tracked.txt"])
	}
	if before["untracked.txt"].mode.Perm() != 0o600 {
		t.Fatalf("untracked mode %o", before["untracked.txt"].mode.Perm())
	}
	if before["link.txt"].body != "link:tracked.txt" {
		t.Fatalf("symlink %#v", before["link.txt"])
	}

	stdout, stderr, code := runCLI(t, "https://example.com/new.git")
	if code != 0 {
		t.Fatalf("code %d\nstderr: %s", code, stderr)
	}
	snapsEqual(t, before, snapshot(t, repo))
	if _, err := gitOut(t, repo, "rev-parse", "--verify", "HEAD"); err == nil {
		t.Fatal("replacement repository has a commit")
	}
	if _, err := gitOut(t, repo, "cat-file", "-e", head+"^{commit}"); err == nil {
		t.Fatal("old commit is still in the replacement repository")
	}
	if _, err := gitOut(t, repo, "diff", "--cached", "--quiet"); err != nil {
		t.Fatalf("index is not empty: %v", err)
	}
	origin := strings.TrimSpace(mustGit(t, repo, "remote", "get-url", "origin"))
	if origin != "https://example.com/new.git" {
		t.Fatalf("origin %s", origin)
	}
	if remotes := strings.TrimSpace(mustGit(t, repo, "remote")); remotes != "origin" {
		t.Fatalf("remotes %q", remotes)
	}
	if branch := strings.TrimSpace(mustGit(t, repo, "symbolic-ref", "--short", "HEAD")); branch != "main" {
		t.Fatalf("branch %s", branch)
	}
	if _, err := gitOut(t, repo, "check-ignore", "ignored.txt"); err != nil {
		t.Fatalf("ignored.txt is not ignored: %v", err)
	}
	backup := backedUpPath(stdout)
	if backup == "" {
		t.Fatalf("stdout missing backup path:\n%s", stdout)
	}
	assertOutside(t, resolved(t, repo), backup)
	if got := strings.TrimSpace(mustGitDir(t, filepath.Join(backup, "git"), "rev-parse", "HEAD")); got != head {
		t.Fatalf("backup HEAD %s, want %s", got, head)
	}
	if _, err := os.Lstat(filepath.Join(repo, rebindOldName)); !os.IsNotExist(err) {
		t.Fatal("temporary metadata was left in the project")
	}
	if !strings.Contains(stdout, "No files were changed. Nothing was committed or pushed.") {
		t.Fatalf("stdout: %s", stdout)
	}
	dirs := backupDirs(t, data)
	if len(dirs) != 1 {
		t.Fatalf("backups %+v", dirs)
	}
}

func TestKeepHistory(t *testing.T) {
	data := testEnv(t)
	repo := initRepo(t)
	chdirTemp(t, repo)
	mustGit(t, repo, "remote", "add", "upstream", "https://example.com/upstream.git")
	head := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))
	before := snapshot(t, repo)

	stdout, stderr, code := runCLI(t, "--keep-history", "https://example.com/new.git")
	if code != 0 {
		t.Fatalf("code %d\nstderr: %s", code, stderr)
	}
	if got := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD")); got != head {
		t.Fatalf("HEAD changed to %s", got)
	}
	if got := strings.TrimSpace(mustGit(t, repo, "remote", "get-url", "origin")); got != "https://example.com/new.git" {
		t.Fatalf("origin %s", got)
	}
	if got := strings.TrimSpace(mustGit(t, repo, "remote", "get-url", "upstream")); got != "https://example.com/upstream.git" {
		t.Fatalf("upstream %s", got)
	}
	snapsEqual(t, before, snapshot(t, repo))
	if len(backupDirs(t, data)) != 0 {
		t.Fatal("keep-history created a backup")
	}
	if !strings.Contains(stdout, "History and working tree were not changed.") {
		t.Fatalf("stdout: %s", stdout)
	}

	stdout, stderr, code = runCLI(t, "--keep-history", "https://example.com/new.git")
	if code != 0 {
		t.Fatalf("code %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "origin already set to https://example.com/new.git") {
		t.Fatalf("stdout: %s", stdout)
	}

	mustGit(t, repo, "remote", "remove", "origin")
	stdout, stderr, code = runCLI(t, "--keep-history", "https://example.com/added.git")
	if code != 0 {
		t.Fatalf("code %d\nstderr: %s", code, stderr)
	}
	if got := strings.TrimSpace(mustGit(t, repo, "remote", "get-url", "origin")); got != "https://example.com/added.git" {
		t.Fatalf("origin %s", got)
	}
	if got := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD")); got != head {
		t.Fatalf("HEAD changed to %s", got)
	}
	if got := strings.TrimSpace(mustGit(t, repo, "remote", "get-url", "upstream")); got != "https://example.com/upstream.git" {
		t.Fatalf("upstream %s", got)
	}
	snapsEqual(t, before, snapshot(t, repo))
	if len(backupDirs(t, data)) != 0 {
		t.Fatal("adding origin created a backup")
	}
}

func TestDryRun(t *testing.T) {
	data := testEnv(t)
	repo := makeRepo(t)
	chdirTemp(t, repo)
	before := snapshot(t, repo)
	config := readBytes(t, filepath.Join(repo, ".git", "config"))
	head := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))

	stdout, stderr, code := runCLI(t, "--dry-run", "https://example.com/new.git")
	if code != 0 {
		t.Fatalf("code %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Would back up") || !strings.Contains(stdout, "Would replace Git metadata") || !strings.Contains(stdout, "https://example.com/new.git") {
		t.Fatalf("stdout: %s", stdout)
	}
	assertDryUnchanged(t, repo, data, before, config, head)

	stdout, stderr, code = runCLI(t, "--dry-run", "--keep-history", "https://example.com/other.git")
	if code != 0 {
		t.Fatalf("code %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Would set origin URL from git@github.com:old/repo.git to https://example.com/other.git") {
		t.Fatalf("stdout: %s", stdout)
	}
	assertDryUnchanged(t, repo, data, before, config, head)
}

func assertDryUnchanged(t *testing.T, repo, data string, before map[string]fileSnap, config []byte, head string) {
	t.Helper()
	snapsEqual(t, before, snapshot(t, repo))
	if got := readBytes(t, filepath.Join(repo, ".git", "config")); string(got) != string(config) {
		t.Fatal(".git/config changed during dry-run")
	}
	if got := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD")); got != head {
		t.Fatalf("HEAD changed to %s", got)
	}
	entries, err := os.ReadDir(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("dry-run wrote %#v", entries)
	}
}

func TestUndoRestoresBackup(t *testing.T) {
	data := testEnv(t)
	repo := makeRepo(t)
	chdirTemp(t, repo)
	head := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))
	before := snapshot(t, repo)
	stdout, stderr, code := runCLI(t, "https://example.com/new.git")
	if code != 0 {
		t.Fatalf("rebind code %d\n%s", code, stderr)
	}
	backup := backedUpPath(stdout)

	stdout, stderr, code = runCLI(t, "--undo", "--dry-run")
	if code != 0 {
		t.Fatalf("dry-run code %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "Confirmation required: no") || !strings.Contains(stdout, backup) {
		t.Fatalf("stdout: %s", stdout)
	}
	if _, err := gitOut(t, repo, "rev-parse", "--verify", "HEAD"); err == nil {
		t.Fatal("dry-run undo created a commit")
	}

	stdout, stderr, code = runCLI(t, "--undo")
	if code != 0 {
		t.Fatalf("undo code %d\n%s", code, stderr)
	}
	if got := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD")); got != head {
		t.Fatalf("HEAD %s, want %s", got, head)
	}
	if got := strings.TrimSpace(mustGit(t, repo, "remote", "get-url", "origin")); got != "git@github.com:old/repo.git" {
		t.Fatalf("origin %s", got)
	}
	snapsEqual(t, before, snapshot(t, repo))
	manifest := readBytes(t, filepath.Join(backup, "manifest.json"))
	if !strings.Contains(string(manifest), `"state": "restored"`) {
		t.Fatalf("manifest: %s", manifest)
	}
	if len(displacedGitDirs(t, data)) == 0 {
		t.Fatal("undo deleted the replacement metadata")
	}

	_, stderr, code = runCLI(t, "--undo")
	if code == 0 || !strings.Contains(stderr, "nothing to undo") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if got := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD")); got != head {
		t.Fatalf("second undo changed HEAD to %s", got)
	}
}

func TestUndoWalksOlderBackups(t *testing.T) {
	testEnv(t)
	repo := makeRepo(t)
	chdirTemp(t, repo)
	head := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))
	before := snapshot(t, repo)

	if _, stderr, code := runCLI(t, "https://example.com/first.git"); code != 0 {
		t.Fatal(stderr)
	}
	if _, stderr, code := runCLI(t, "https://example.com/second.git"); code != 0 {
		t.Fatal(stderr)
	}
	if _, stderr, code := runCLI(t, "--undo"); code != 0 {
		t.Fatal(stderr)
	}
	if _, err := gitOut(t, repo, "rev-parse", "--verify", "HEAD"); err == nil {
		t.Fatal("first undo restored the original history")
	}
	if got := strings.TrimSpace(mustGit(t, repo, "remote", "get-url", "origin")); got != "https://example.com/first.git" {
		t.Fatalf("origin after first undo %s", got)
	}
	if _, stderr, code := runCLI(t, "--undo"); code != 0 {
		t.Fatal(stderr)
	}
	if got := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD")); got != head {
		t.Fatalf("HEAD %s, want %s", got, head)
	}
	if got := strings.TrimSpace(mustGit(t, repo, "remote", "get-url", "origin")); got != "git@github.com:old/repo.git" {
		t.Fatalf("origin %s", got)
	}
	snapsEqual(t, before, snapshot(t, repo))
}

func TestUndoRefusesChangedMetadataWithoutForce(t *testing.T) {
	data := testEnv(t)
	repo := initRepo(t)
	chdirTemp(t, repo)
	oldHead := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))
	if _, stderr, code := runCLI(t, "https://example.com/new.git"); code != 0 {
		t.Fatal(stderr)
	}
	writeFile(t, filepath.Join(repo, "new.txt"), "new\n", 0o644)
	mustGit(t, repo, "add", "new.txt")
	mustGit(t, repo, "commit", "-m", "after rebind")
	newHead := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))
	before := snapshot(t, repo)

	prevTerm := cliIsTerm
	cliIsTerm = func() bool { return false }
	t.Cleanup(func() { cliIsTerm = prevTerm })

	stdout, stderr, code := runCLI(t, "--undo", "--dry-run")
	if code != 0 {
		t.Fatalf("dry-run code %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "Confirmation required: yes") {
		t.Fatalf("stdout: %s", stdout)
	}
	if got := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD")); got != newHead {
		t.Fatalf("dry-run changed HEAD to %s", got)
	}

	_, stderr, code = runCLI(t, "--undo")
	if code == 0 || !strings.Contains(stderr, "re-run with --force") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if got := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD")); got != newHead {
		t.Fatalf("HEAD %s, want %s", got, newHead)
	}
	snapsEqual(t, before, snapshot(t, repo))

	stdout, stderr, code = runCLI(t, "--undo", "--force")
	if code != 0 {
		t.Fatalf("force code %d\n%s", code, stderr)
	}
	if got := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD")); got != oldHead {
		t.Fatalf("HEAD %s, want %s", got, oldHead)
	}
	if got := strings.TrimSpace(mustGit(t, repo, "remote", "get-url", "origin")); got != "git@github.com:old/repo.git" {
		t.Fatalf("origin %s", got)
	}
	snapsEqual(t, before, snapshot(t, repo))
	found := false
	for _, dir := range displacedGitDirs(t, data) {
		if strings.TrimSpace(mustGitDir(t, dir, "rev-parse", "HEAD")) == newHead {
			found = true
		}
	}
	if !found {
		t.Fatal("displaced metadata does not contain the newer commit")
	}
}

func TestRebindRollsBackWhenRemoteAddFails(t *testing.T) {
	data := testEnv(t)
	repo := makeRepo(t)
	chdirTemp(t, repo)
	head := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))
	before := snapshot(t, repo)
	origin := strings.TrimSpace(mustGit(t, repo, "remote", "get-url", "origin"))
	prependGitStub(t, `#!/bin/sh
prev=
for arg in "$@"; do
  if [ "$prev" = "remote" ] && [ "$arg" = "add" ]; then
    echo "stub refused remote add" >&2
    exit 1
  fi
  prev=$arg
done
exec {{GIT}} "$@"
`)

	_, stderr, code := runCLI(t, "https://example.com/new.git")
	if code == 0 || !strings.Contains(stderr, "stub refused remote add") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if got := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD")); got != head {
		t.Fatalf("HEAD %s, want %s", got, head)
	}
	if got := strings.TrimSpace(mustGit(t, repo, "remote", "get-url", "origin")); got != origin {
		t.Fatalf("origin %s", got)
	}
	snapsEqual(t, before, snapshot(t, repo))
	if _, err := os.Lstat(filepath.Join(repo, rebindOldName)); !os.IsNotExist(err) {
		t.Fatal("rollback left .git.rebind-old in place")
	}
	if len(backupDirs(t, data)) != 0 {
		t.Fatal("failed rebind left a backup behind")
	}
}

func TestRebindRefusesEscapingSymlink(t *testing.T) {
	data := testEnv(t)
	repo := initRepo(t)
	chdirTemp(t, repo)
	if err := os.Symlink("../outside", filepath.Join(repo, ".git", "escape-link")); err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))
	_, stderr, code := runCLI(t, "https://example.com/new.git")
	if code == 0 || !strings.Contains(stderr, "symlink") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if got := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD")); got != head {
		t.Fatalf("HEAD %s, want %s", got, head)
	}
	if len(backupDirs(t, data)) != 0 {
		t.Fatal("refused rebind left a backup behind")
	}
}

func TestRefuseBare(t *testing.T) {
	testEnv(t)
	dir := t.TempDir()
	mustGit(t, dir, "init", "--bare", "-b", "main")
	chdirTemp(t, dir)
	_, stderr, code := runCLI(t, "https://example.com/new.git")
	if code == 0 || !strings.Contains(stderr, "bare") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
}

func TestRefuseWorktree(t *testing.T) {
	testEnv(t)
	repo := initRepo(t)
	worktree := filepath.Join(t.TempDir(), "wt")
	mustGit(t, repo, "worktree", "add", worktree, "HEAD")
	config := readBytes(t, filepath.Join(repo, ".git", "config"))

	chdirTemp(t, repo)
	_, stderr, code := runCLI(t, "https://example.com/new.git")
	if code == 0 || !strings.Contains(stderr, "multiple worktrees") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if got := readBytes(t, filepath.Join(repo, ".git", "config")); string(got) != string(config) {
		t.Fatal("main repository config changed")
	}

	chdirTemp(t, worktree)
	_, stderr, code = runCLI(t, "https://example.com/new.git")
	if code == 0 || !strings.Contains(stderr, "not a directory") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if got := readBytes(t, filepath.Join(repo, ".git", "config")); string(got) != string(config) {
		t.Fatal("worktree rebind changed the main config")
	}
}

func TestRefuseSubmodule(t *testing.T) {
	testEnv(t)
	remote := t.TempDir()
	mustGit(t, remote, "init", "-b", "main")
	writeFile(t, filepath.Join(remote, "lib.txt"), "lib\n", 0o644)
	mustGit(t, remote, "add", "lib.txt")
	mustGit(t, remote, "commit", "-m", "lib")

	parent := t.TempDir()
	mustGit(t, parent, "init", "-b", "main")
	writeFile(t, filepath.Join(parent, "README"), "parent\n", 0o644)
	mustGit(t, parent, "add", "README")
	mustGit(t, parent, "commit", "-m", "parent")
	mustGit(t, parent, "-c", "protocol.file.allow=always", "submodule", "add", remote, "child")
	config := readBytes(t, filepath.Join(parent, ".git", "config"))
	childGit := readBytes(t, filepath.Join(parent, "child", ".git"))

	chdirTemp(t, parent)
	_, stderr, code := runCLI(t, "https://example.com/new.git")
	if code == 0 || !strings.Contains(stderr, "submodule") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if got := readBytes(t, filepath.Join(parent, ".git", "config")); string(got) != string(config) {
		t.Fatal("parent config changed")
	}

	chdirTemp(t, filepath.Join(parent, "child"))
	_, stderr, code = runCLI(t, "https://example.com/new.git")
	if code == 0 || !strings.Contains(stderr, "not a directory") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if got := readBytes(t, filepath.Join(parent, "child", ".git")); string(got) != string(childGit) {
		t.Fatal("submodule gitlink changed")
	}
}

func TestRefuseNotARepository(t *testing.T) {
	testEnv(t)
	dir := t.TempDir()
	chdirTemp(t, dir)
	_, stderr, code := runCLI(t, "https://example.com/new.git")
	if code == 0 || !strings.Contains(stderr, "not a git repository") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Fatal("created .git in a non-repository")
	}
}

func TestRefuseOldGit(t *testing.T) {
	testEnv(t)
	repo := initRepo(t)
	chdirTemp(t, repo)
	head := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))
	config := readBytes(t, filepath.Join(repo, ".git", "config"))
	prependGitStub(t, `#!/bin/sh
if [ "$1" = "version" ]; then
  echo "git version 2.20.0"
  exit 0
fi
exec {{GIT}} "$@"
`)
	_, stderr, code := runCLI(t, "https://example.com/new.git")
	if code == 0 || !strings.Contains(stderr, "2.28") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if got := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD")); got != head {
		t.Fatalf("HEAD %s", got)
	}
	if got := readBytes(t, filepath.Join(repo, ".git", "config")); string(got) != string(config) {
		t.Fatal("config changed")
	}
}

func TestStatus(t *testing.T) {
	data := testEnv(t)
	repo := initRepo(t)
	chdirTemp(t, repo)
	head := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))
	root := resolved(t, repo)

	stdout, stderr, code := runCLI(t, "--status")
	if code != 0 {
		t.Fatal(stderr)
	}
	for _, want := range []string{
		"Repository: " + root,
		"Branch: main",
		"Origin: git@github.com:old/repo.git",
		"HEAD: " + head,
		"Backups:\n  (none)",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("missing %q in\n%s", want, stdout)
		}
	}

	if _, stderr, code := runCLI(t, "https://example.com/new.git"); code != 0 {
		t.Fatal(stderr)
	}
	stdout, stderr, code = runCLI(t, "--status")
	if code != 0 {
		t.Fatal(stderr)
	}
	if !strings.Contains(stdout, "Origin: https://example.com/new.git") || !strings.Contains(stdout, "available") {
		t.Fatalf("stdout: %s", stdout)
	}
	if len(backupDirs(t, data)) != 1 {
		t.Fatal("status lost the backup")
	}
}

func TestStatusWithoutRepository(t *testing.T) {
	testEnv(t)
	dir := t.TempDir()
	chdirTemp(t, dir)
	stdout, stderr, code := runCLI(t, "--status")
	if code != 0 {
		t.Fatal(stderr)
	}
	if !strings.Contains(stdout, "Not a Git repository") || !strings.Contains(stdout, "(none)") {
		t.Fatalf("stdout: %s", stdout)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Fatal("status created .git")
	}
}

func TestHelpAndVersion(t *testing.T) {
	stdout, stderr, code := runCLI(t, "--help")
	if code != 0 {
		t.Fatal(stderr)
	}
	if !strings.Contains(stdout, "--keep-history") || !strings.Contains(stdout, "--undo") {
		t.Fatalf("stdout: %s", stdout)
	}
	stdout, stderr, code = runCLI(t, "--version")
	if code != 0 {
		t.Fatal(stderr)
	}
	if stdout != "git-rebind dev\n" {
		t.Fatalf("version %q", stdout)
	}
}

func TestUsageErrors(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--status", "https://example.com/new.git"}, "--status"},
		{[]string{"--undo", "https://example.com/new.git"}, "--undo"},
		{[]string{"--version", "--status"}, "--version"},
		{[]string{"--keep-history"}, "--keep-history"},
		{[]string{"--force"}, "--force"},
		{[]string{"https://example.com/a.git", "https://example.com/b.git"}, "exactly one remote URL"},
		{[]string{" https://example.com/a.git"}, "whitespace"},
		{[]string{"--", "-evil"}, "must not start"},
		{[]string{"https://example.com/a.git\nbad"}, "control character"},
		{[]string{}, "exactly one remote URL"},
	}
	for _, tc := range cases {
		_, stderr, code := runCLI(t, tc.args...)
		if code != 2 || !strings.Contains(stderr, tc.want) {
			t.Fatalf("args %q code %d stderr %s", tc.args, code, stderr)
		}
	}
}

func TestParseGitVersion(t *testing.T) {
	major, minor, err := parseGitVersion("git version 2.39.5 (Apple Git-154)\n")
	if err != nil || major != 2 || minor != 39 {
		t.Fatalf("got %d.%d %v", major, minor, err)
	}
	major, minor, err = parseGitVersion("git version 2.28.0")
	if err != nil || major != 2 || minor != 28 {
		t.Fatalf("got %d.%d %v", major, minor, err)
	}
	if _, _, err := parseGitVersion("not git"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestConfirm(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{"n\n", false},
		{"\n", false},
		{"", false},
	}
	for _, tc := range cases {
		got, err := confirm(strings.NewReader(tc.in))
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Fatalf("confirm %q = %v", tc.in, got)
		}
	}
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
