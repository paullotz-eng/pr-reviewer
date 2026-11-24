package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

// checkout creates a temporary detached worktree of the PR head in clone.
// The caller must call cleanup, which removes the worktree again.
func checkout(ctx context.Context, clone string, n int, sha string) (dir string, cleanup func(), err error) {
	if git(ctx, clone, "cat-file", "-e", sha+"^{commit}") != nil {
		if err := git(ctx, clone, "fetch", "--no-tags", "--quiet", "origin", fmt.Sprintf("pull/%d/head", n)); err != nil {
			return "", nil, err
		}
	}
	dir, err = os.MkdirTemp("", "pr-reviewer-")
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
