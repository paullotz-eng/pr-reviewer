package main

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"time"
)

// runner makes sure a PR is reviewed only once at a time: a second request for
// the same PR waits for the running review instead of starting another.
type runner struct {
	mu      sync.Mutex
	running map[string]*call
}

type call struct {
	done chan struct{}
	rev  *Review
	err  error
}

func (r *runner) do(key string, fn func() (*Review, error)) (*Review, error) {
	r.mu.Lock()
	if c, ok := r.running[key]; ok {
		r.mu.Unlock()
		<-c.done
		return c.rev, c.err
	}
	c := &call{done: make(chan struct{})}
	r.running[key] = c
	r.mu.Unlock()

	c.rev, c.err = fn()
	r.mu.Lock()
	delete(r.running, key)
	r.mu.Unlock()
	close(c.done)
	return c.rev, c.err
}

func (r *runner) isRunning(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running[key] != nil
}

// watcher polls for open PRs and reviews the new ones by team members.
type watcher struct {
	every   time.Duration
	since   time.Time // only PRs opened after this are reviewed automatically
	repo    string
	org     string
	me      string
	members map[string]bool
	review  func(ctx context.Context, repo string, n int) (*Review, error)
	known   func(key string) bool // already reviewed or running
}

// due reports whether a PR should be reviewed automatically.
func (w *watcher) due(pr PR) bool {
	return w.members[pr.Author] && pr.Author != w.me && !pr.IsDraft &&
		pr.CreatedAt.After(w.since) && !w.known(prKey(pr.Repo, pr.Number))
}

func (w *watcher) run(ctx context.Context) {
	for {
		w.poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.every):
		}
	}
}

func (w *watcher) poll(ctx context.Context) {
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	// draft:false makes it a search: within -repo, or across the team's org.
	prs, err := listPRs(lctx, w.repo, w.org, "draft:false", 50)
	cancel()
	if err != nil {
		log.Printf("watch: %v", err)
		return
	}
	// Oldest first, one at a time, so a burst of PRs doesn't start many Claude runs at once.
	for i := len(prs) - 1; i >= 0; i-- {
		pr := prs[i]
		if !w.due(pr) {
			continue
		}
		key := prKey(pr.Repo, pr.Number)
		log.Printf("watch: new PR by @%s, reviewing %s ...", pr.Author, key)
		rctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		rev, err := w.review(rctx, pr.Repo, pr.Number)
		cancel()
		if err != nil {
			log.Printf("watch: %s: %v", key, err)
			continue
		}
		notify(fmt.Sprintf("%s: %s", key, verdictText(rev.Verdict)),
			fmt.Sprintf("@%s: %s (%d comments)", pr.Author, pr.Title, len(rev.Comments)))
	}
}

func prKey(repo string, n int) string { return repo + "#" + strconv.Itoa(n) }

func verdictText(v string) string {
	switch v {
	case "request_changes":
		return "request changes"
	case "":
		return "reviewed"
	}
	return v
}

// notify shows a desktop notification. Title and body are passed as arguments,
// never spliced into the script, because PR titles are untrusted.
func notify(title, body string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("osascript", "-e", "on run argv", "-e",
			"display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", title, body)
	case "linux":
		cmd = exec.Command("notify-send", "--", title, body)
	default:
		return
	}
	if err := cmd.Run(); err != nil {
		log.Printf("notify: %v", err)
	}
}

// reviewStatus summarises a PR's review for the list.
func reviewStatus(rev *Review, running bool) (text, kind string) {
	switch {
	case running:
		return "reviewing…", "running"
	case rev == nil:
		return "", ""
	case slices.ContainsFunc(rev.Comments, func(c Comment) bool { return c.PostedURL != "" }):
		return "✓ posted", "posted"
	}
	blocking := 0
	for _, c := range rev.Comments {
		if c.Decoration == "blocking" {
			blocking++
		}
	}
	switch n := len(rev.Comments); {
	case blocking > 0:
		return fmt.Sprintf("%d blocking", blocking), "blocking"
	case n == 0:
		return "looks good", "clean"
	case n == 1:
		return "1 comment", "comments"
	default:
		return fmt.Sprintf("%d comments", n), "comments"
	}
}
