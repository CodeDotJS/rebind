package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const minGitMinor = 28

var errNotRepository = errors.New("not a git repository")

type layoutError struct {
	reason string
}

func (e *layoutError) Error() string { return e.reason }

// Repo is a normal worktree git-rebind is willing to modify.
type Repo struct {
	Root     string
	GitDir   string
	Branch   string
	Detached bool
	Origin   string
	Head     string
	Staged   bool
}

func (r Repo) changed() bool {
	return r.Head != "" || r.Staged
}

func gitEnv() []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if dropGitEnv(entry) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, "GIT_TERMINAL_PROMPT=0")
}

func dropGitEnv(entry string) bool {
	switch {
	case strings.HasPrefix(entry, "GIT_DIR="),
		strings.HasPrefix(entry, "GIT_WORK_TREE="),
		strings.HasPrefix(entry, "GIT_INDEX_FILE="),
		strings.HasPrefix(entry, "GIT_OBJECT_DIRECTORY="),
		strings.HasPrefix(entry, "GIT_COMMON_DIR="),
		strings.HasPrefix(entry, "GIT_NAMESPACE="):
		return true
	default:
		return false
	}
}

func runGitCode(ctx context.Context, dir string, args ...string) (string, string, int, error) {
	bin, err := exec.LookPath("git")
	if err != nil {
		return "", "", -1, errors.New("git is not installed or not on PATH")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	out := stdout.String()
	errOut := stderr.String()
	if err == nil {
		return out, errOut, 0, nil
	}
	if ctx.Err() != nil {
		return out, errOut, -1, fmt.Errorf("git %s: %w", args[0], ctx.Err())
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out, errOut, exitErr.ExitCode(), nil
	}
	return out, errOut, -1, fmt.Errorf("git %s: %w", args[0], err)
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	stdout, stderr, code, err := runGitCode(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	if code != 0 {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = fmt.Sprintf("exit status %d", code)
		}
		return stdout, fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout, nil
}

func checkGitVersion(ctx context.Context, dir string) error {
	out, err := runGit(ctx, dir, "version")
	if err != nil {
		return err
	}
	major, minor, err := parseGitVersion(out)
	if err != nil {
		return fmt.Errorf("cannot parse git version %q", strings.TrimSpace(out))
	}
	if major < 2 || (major == 2 && minor < minGitMinor) {
		return fmt.Errorf("git 2.%d or newer is required (found %d.%d)", minGitMinor, major, minor)
	}
	return nil
}

func parseGitVersion(out string) (int, int, error) {
	fields := strings.Fields(out)
	if len(fields) < 3 {
		return 0, 0, errors.New("unexpected git version output")
	}
	parts := strings.Split(fields[2], ".")
	if len(parts) < 2 {
		return 0, 0, errors.New("unexpected git version number")
	}
	var major, minor int
	if _, err := fmt.Sscan(parts[0], &major); err != nil {
		return 0, 0, err
	}
	if _, err := fmt.Sscan(parts[1], &minor); err != nil {
		return 0, 0, err
	}
	return major, minor, nil
}

func detect(ctx context.Context, start string) (Repo, error) {
	if err := checkGitVersion(ctx, start); err != nil {
		return Repo{}, err
	}
	work, err := revParseBool(ctx, start, "--is-inside-work-tree")
	if errors.Is(err, errNotRepository) {
		return Repo{}, errNotRepository
	}
	if err != nil {
		return Repo{}, err
	}
	bare, err := revParseBool(ctx, start, "--is-bare-repository")
	if errors.Is(err, errNotRepository) {
		return Repo{}, errNotRepository
	}
	if err != nil {
		return Repo{}, err
	}
	if bare {
		abs, absErr := evalPath(start)
		if absErr != nil {
			return Repo{}, absErr
		}
		return Repo{Root: abs, GitDir: abs}, &layoutError{reason: "bare repositories are not supported"}
	}
	inGit, err := revParseBool(ctx, start, "--is-inside-git-dir")
	if errors.Is(err, errNotRepository) {
		return Repo{}, errNotRepository
	}
	if err != nil {
		return Repo{}, err
	}
	if inGit && !work {
		abs, absErr := evalPath(start)
		if absErr != nil {
			return Repo{}, absErr
		}
		return Repo{Root: abs}, &layoutError{reason: "refusing to run inside .git"}
	}
	if !work {
		return Repo{}, errNotRepository
	}

	rootOut, err := runGit(ctx, start, "rev-parse", "--show-toplevel")
	if err != nil {
		return Repo{}, err
	}
	root, err := evalPath(strings.TrimSpace(rootOut))
	if err != nil {
		return Repo{}, fmt.Errorf("resolve worktree: %w", err)
	}
	gitOut, err := runGit(ctx, start, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return Repo{}, err
	}
	gitDir, err := evalPath(strings.TrimSpace(gitOut))
	if err != nil {
		return Repo{}, fmt.Errorf("resolve git directory: %w", err)
	}
	repo := Repo{Root: root, GitDir: gitDir}
	if err := inspect(ctx, &repo); err != nil {
		return repo, err
	}
	if err := validateLayout(ctx, &repo); err != nil {
		return repo, err
	}
	return repo, nil
}

func revParseBool(ctx context.Context, dir, flagName string) (bool, error) {
	out, stderr, code, err := runGitCode(ctx, dir, "rev-parse", flagName)
	if err != nil {
		return false, err
	}
	if code != 0 {
		if code == 128 && strings.Contains(strings.ToLower(stderr), "not a git repository") {
			return false, errNotRepository
		}
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = fmt.Sprintf("exit status %d", code)
		}
		return false, fmt.Errorf("git rev-parse %s: %s", flagName, msg)
	}
	switch strings.TrimSpace(out) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("git rev-parse %s returned %q", flagName, strings.TrimSpace(out))
	}
}

func inspect(ctx context.Context, repo *Repo) error {
	out, _, code, err := runGitCode(ctx, repo.Root, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return err
	}
	if code == 0 {
		repo.Branch = strings.TrimSpace(out)
		repo.Detached = false
	} else {
		repo.Branch = ""
		repo.Detached = true
	}

	out, _, code, err = runGitCode(ctx, repo.Root, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		return err
	}
	if code == 0 {
		repo.Head = strings.TrimSpace(out)
	} else {
		repo.Head = ""
	}

	out, _, code, err = runGitCode(ctx, repo.Root, "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	if code == 0 {
		repo.Origin = strings.TrimSpace(out)
	} else {
		repo.Origin = ""
	}

	_, stderr, code, err := runGitCode(ctx, repo.Root, "diff", "--cached", "--quiet")
	if err != nil {
		return err
	}
	switch code {
	case 0:
		repo.Staged = false
	case 1:
		repo.Staged = true
	default:
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = fmt.Sprintf("exit status %d", code)
		}
		return fmt.Errorf("check staged changes: %s", msg)
	}
	return nil
}

func validateLayout(ctx context.Context, repo *Repo) error {
	gitPath := filepath.Join(repo.Root, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		return fmt.Errorf("inspect .git: %w", err)
	}
	if !info.IsDir() {
		return &layoutError{reason: ".git is not a directory (linked worktrees and submodules are not supported)"}
	}
	linked, err := evalPath(gitPath)
	if err != nil {
		return fmt.Errorf("resolve .git: %w", err)
	}
	if linked != repo.GitDir {
		return &layoutError{reason: "refusing to use a git directory outside the worktree"}
	}
	repo.GitDir = gitPath

	if _, err := os.Lstat(filepath.Join(gitPath, "commondir")); err == nil {
		return &layoutError{reason: "linked worktrees are not supported"}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect .git/commondir: %w", err)
	}

	super, stderr, code, err := runGitCode(ctx, repo.Root, "rev-parse", "--show-superproject-working-tree")
	if err != nil {
		return err
	}
	if code != 0 {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = fmt.Sprintf("exit status %d", code)
		}
		return fmt.Errorf("check whether this is a submodule: %s", msg)
	}
	if strings.TrimSpace(super) != "" {
		return &layoutError{reason: "submodules are not supported"}
	}

	if _, err := os.Lstat(filepath.Join(repo.Root, ".gitmodules")); err == nil {
		return &layoutError{reason: "repositories that contain submodules are not supported"}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect .gitmodules: %w", err)
	}
	if _, err := os.Lstat(filepath.Join(gitPath, "modules")); err == nil {
		return &layoutError{reason: "repositories that contain submodules are not supported"}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect .git/modules: %w", err)
	}

	count, err := worktreeCount(ctx, repo.Root)
	if err != nil {
		return err
	}
	if count == 0 {
		return &layoutError{reason: "could not identify a worktree"}
	}
	if count > 1 {
		return &layoutError{reason: "repositories with multiple worktrees are not supported"}
	}
	return nil
}

func worktreeCount(ctx context.Context, root string) (int, error) {
	out, err := runGit(ctx, root, "worktree", "list", "--porcelain")
	if err != nil {
		return 0, err
	}
	count := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			count++
		}
	}
	return count, nil
}

func isLayout(err error) bool {
	var layout *layoutError
	return errors.As(err, &layout)
}

func evalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func workingPath() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	resolved, err := evalPath(wd)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	return resolved, nil
}
