package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	clonesMu sync.Mutex
	clones   = map[string]string{} // repo -> local clone dir ("" = none)
)

// findClone returns a local clone of repo among the direct subdirectories of root.
func findClone(root, repo string) string {
	clonesMu.Lock()
	defer clonesMu.Unlock()
	if dir, ok := clones[repo]; ok {
		return dir
	}
	clones[repo] = ""
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		if !e.IsDir() {
			continue
		}
		out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
		if err == nil && remoteMatches(strings.TrimSpace(string(out)), repo) {
			clones[repo] = dir
			break
		}
	}
	return clones[repo]
}

// remoteMatches reports whether a git remote URL (ssh or https) points at owner/name.
func remoteMatches(url, repo string) bool {
	u := strings.ToLower(strings.TrimSuffix(url, ".git"))
	r := strings.ToLower(repo)
	return strings.HasSuffix(u, "/"+r) || strings.HasSuffix(u, ":"+r)
}

// git runs git in dir with hooks disabled, so checkouts don't trigger husky & co.
func git(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

const checkoutPrefix = "pr-reviewer-"

// cleanStaleCheckouts removes worktrees in root left behind by a run that was
// killed mid-review. Younger ones may belong to another running instance (a
// review times out after 15 minutes), so only those older than minAge go.
func cleanStaleCheckouts(root string, minAge time.Duration) {
	dirs, _ := filepath.Glob(filepath.Join(root, checkoutPrefix+"*"))
	for _, dir := range dirs {
		fi, err := os.Stat(dir)
		if err != nil || !fi.IsDir() || time.Since(fi.ModTime()) < minAge {
			continue
		}
		// A worktree's .git file reads "gitdir: <clone>/.git/worktrees/<name>".
		clone := ""
		if b, err := os.ReadFile(filepath.Join(dir, ".git")); err == nil {
			gitdir := strings.TrimSpace(strings.TrimPrefix(string(b), "gitdir:"))
			if i := strings.Index(gitdir, "/.git/worktrees/"); i >= 0 {
				clone = gitdir[:i]
			}
		}
		if clone != "" {
			git(context.Background(), clone, "worktree", "remove", "--force", dir)
		}
		os.RemoveAll(dir)
		if clone != "" {
			git(context.Background(), clone, "worktree", "prune")
		}
		log.Printf("removed stale checkout %s", dir)
	}
}

// checkout creates a temporary detached worktree of the PR head in clone.
// The caller must call cleanup, which removes the worktree again.
func checkout(ctx context.Context, clone string, n int, sha string) (dir string, cleanup func(), err error) {
	if git(ctx, clone, "cat-file", "-e", sha+"^{commit}") != nil {
		if err := git(ctx, clone, "fetch", "--no-tags", "--quiet", "origin", fmt.Sprintf("pull/%d/head", n)); err != nil {
			return "", nil, err
		}
	}
	dir, err = os.MkdirTemp("", checkoutPrefix)
	if err != nil {
		return "", nil, err
	}
	if err := git(ctx, clone, "worktree", "add", "--detach", "--quiet", dir, sha); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	return dir, func() {
		git(context.Background(), clone, "worktree", "remove", "--force", dir)
		os.RemoveAll(dir)
	}, nil
}
