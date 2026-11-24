package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var indexHTML []byte

func main() {
	home, _ := os.UserHomeDir()
	cacheDir, _ := os.UserCacheDir()
	addr := flag.String("addr", "127.0.0.1:7777", "listen address")
	repo := flag.String("repo", "", "owner/name to list PRs from (default: PRs where your review is requested)")
	limit := flag.Int("limit", 30, "number of PRs to list")
	model := flag.String("model", "", "claude model to review with (default: claude CLI default)")
	team := flag.String("team", "eversport/team-ball-racket-sports", "org/team-slug whose PRs get a team tag (\"\" to disable)")
	clonesRoot := flag.String("clones", filepath.Join(home, "Code"), "directory with local clones; a matching clone gives the reviewer codebase access")
	skillFile := flag.String("skill-file", filepath.Join(home, "Code/eversports", conventionsSkill),
		"local react-conventions skill, used if the PR's repo has none")
	storePath := flag.String("store", filepath.Join(cacheDir, "pr-reviewer", "reviews.json"), "file that keeps reviews across restarts")
	noOpen := flag.Bool("no-open", false, "don't open the browser")
	watch := flag.Duration("watch", 5*time.Minute, "how often to check for new PRs by -team members and review them automatically (0 disables)")
	flag.Parse()
	opts := Options{Model: *model, SkillFile: *skillFile, ClonesRoot: *clonesRoot}

	startCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	me, err := currentUser(startCtx)
	if err != nil {
		log.Print(err)
	}
	members, org := map[string]bool{}, ""
	if *team != "" {
		org, _, _ = strings.Cut(*team, "/")
		if members, err = teamMembers(startCtx, *team); err != nil {
			log.Printf("team tagging disabled: %v", err)
		} else {
			log.Printf("team %s: %d members", *team, len(members))
		}
	}
	cancel()

	st := loadStore(*storePath)
	runs := &runner{running: map[string]*call{}}
	// reviewPR runs a review (or waits for the one already running) and stores it.
	reviewPR := func(ctx context.Context, repo string, n int) (*Review, error) {
		key := prKey(repo, n)
		return runs.do(key, func() (*Review, error) {
			log.Printf("reviewing %s ...", key)
			start := time.Now()
			rev, err := runReview(ctx, opts, repo, n)
			if err != nil {
				return nil, err
			}
			log.Printf("reviewed %s in %s (%s): %d comments", key, time.Since(start).Round(time.Second), rev.Context, len(rev.Comments))
			st.put(key, rev)
			return st.get(key), nil
		})
	}

	if *watch > 0 && len(members) > 0 {
		w := &watcher{every: *watch, since: time.Now().Add(-24 * time.Hour), repo: *repo, org: org, me: me, members: members,
			review: reviewPR,
			known:  func(key string) bool { return st.get(key) != nil || runs.isRunning(key) }}
		go w.run(context.Background())
		log.Printf("watching for new PRs by %s every %s", *team, *watch)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})

	mux.HandleFunc("GET /api/prs", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		prs, err := listPRs(ctx, *repo, org, strings.TrimSpace(r.URL.Query().Get("q")), *limit)
		for i := range prs {
			prs[i].Mine = prs[i].Author == me
			prs[i].Team = members[prs[i].Author]
			key := prKey(prs[i].Repo, prs[i].Number)
			if runs.isRunning(key) {
				prs[i].Review = "running"
			} else if rev := st.get(key); rev != nil {
				prs[i].Review = rev.Verdict
			}
		}
		reply(w, prs, err)
	})

	mux.HandleFunc("POST /api/review", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Repo   string `json:"repo"`
			Number int    `json:"number"`
			Force  bool   `json:"force"`
		}
		if !decode(w, r, &req) {
			return
		}
		key := req.Repo + "#" + strconv.Itoa(req.Number)
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
		defer cancel()
		sha, err := prHeadSHA(ctx, req.Repo, req.Number)
		if err != nil {
			reply(w, nil, err)
			return
		}
		type resp struct {
			*Review
			Outdated bool `json:"outdated"` // new commits since the review
		}
		if cached := st.get(key); cached != nil && !req.Force {
			reply(w, resp{cached, cached.HeadSHA != sha}, nil)
			return
		}
		rev, err := reviewPR(ctx, req.Repo, req.Number)
		if err != nil {
			reply(w, nil, err)
			return
		}
		reply(w, resp{rev, rev.HeadSHA != sha}, nil)
	})

	// Post one comment right away.
	mux.HandleFunc("POST /api/comment", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Repo   string `json:"repo"`
			Number int    `json:"number"`
			Index  int    `json:"index"`
			Body   string `json:"body"` // possibly edited
		}
		if !decode(w, r, &req) {
			return
		}
		key := req.Repo + "#" + strconv.Itoa(req.Number)
		rev := st.get(key)
		if rev == nil || req.Index < 0 || req.Index >= len(rev.Comments) {
			reply(w, nil, errors.New("unknown review or comment, re-run the review"))
			return
		}
		c := rev.Comments[req.Index]
		c.Body = req.Body
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		url, err := postComment(ctx, req.Repo, req.Number, rev.HeadSHA, c)
		if err == nil {
			log.Printf("posted comment on %s: %s", key, url)
			st.markPosted(key, map[int]string{req.Index: req.Body}, url)
		}
		reply(w, map[string]string{"url": url}, err)
	})

	// Submit selected comments as one GitHub review.
	mux.HandleFunc("POST /api/submit", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Repo     string `json:"repo"`
			Number   int    `json:"number"`
			Event    string `json:"event"`
			Body     string `json:"body"`
			Comments []struct {
				Index int    `json:"index"`
				Body  string `json:"body"`
			} `json:"comments"`
		}
		if !decode(w, r, &req) {
			return
		}
		if !slices.Contains([]string{"COMMENT", "APPROVE", "REQUEST_CHANGES"}, req.Event) {
			reply(w, nil, fmt.Errorf("invalid event %q", req.Event))
			return
		}
		key := req.Repo + "#" + strconv.Itoa(req.Number)
		rev := st.get(key)
		if rev == nil {
			reply(w, nil, errors.New("unknown review, re-run it"))
			return
		}
		var comments []Comment
		bodies := map[int]string{}
		for _, sel := range req.Comments {
			if sel.Index < 0 || sel.Index >= len(rev.Comments) {
				reply(w, nil, fmt.Errorf("unknown comment %d", sel.Index))
				return
			}
			c := rev.Comments[sel.Index]
			c.Body = sel.Body
			comments = append(comments, c)
			bodies[sel.Index] = sel.Body
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		url, err := submitReview(ctx, req.Repo, req.Number, rev.HeadSHA, req.Event, req.Body, comments)
		if err == nil {
			log.Printf("submitted %s review on %s with %d comments: %s", req.Event, key, len(comments), url)
			st.markPosted(key, bodies, url)
		}
		reply(w, map[string]string{"url": url}, err)
	})

	u := "http://" + *addr
	log.Printf("PR reviewer running at %s", u)
	if !*noOpen {
		go openBrowser(u)
	}
	log.Fatal(http.ListenAndServe(*addr, guard(*addr, mux)))
}

// guard blocks requests from other websites: DNS rebinding (foreign Host) and
// cross-site POSTs (foreign Origin, or a non-JSON body that skips CORS preflight).
func guard(addr string, h http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(addr)
	local := func(hostport string) bool {
		host, p, err := net.SplitHostPort(hostport)
		return err == nil && p == port && (host == "127.0.0.1" || host == "localhost" || host == "::1")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !local(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			o, err := url.Parse(r.Header.Get("Origin"))
			if err != nil || o.Scheme != "http" || !local(o.Host) {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

// store keeps the latest review per PR and persists it to disk.
type store struct {
	mu      sync.Mutex
	path    string
	reviews map[string]*Review // "repo#n" -> latest review
}

func loadStore(path string) *store {
	s := &store{path: path, reviews: map[string]*Review{}}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &s.reviews); err != nil {
			log.Printf("ignoring corrupt %s: %v", path, err)
		}
	}
	return s
}

// get returns a copy, so callers can read it without holding the lock.
func (s *store) get(key string) *Review {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.reviews[key]
	if r == nil {
		return nil
	}
	c := *r
	c.Comments = slices.Clone(r.Comments)
	return &c
}

func (s *store) put(key string, r *Review) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reviews[key] = r
	s.save()
}

// markPosted records the posted (possibly edited) bodies and where they went.
func (s *store) markPosted(key string, bodies map[int]string, url string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.reviews[key]
	if r == nil {
		return
	}
	for i, body := range bodies {
		if i >= 0 && i < len(r.Comments) {
			r.Comments[i].Body, r.Comments[i].PostedURL = body, url
		}
	}
	s.save()
}

func (s *store) save() {
	b, _ := json.Marshal(s.reviews)
	err := os.MkdirAll(filepath.Dir(s.path), 0o700)
	if err == nil {
		err = os.WriteFile(s.path, b, 0o600)
	}
	if err != nil {
		log.Printf("saving reviews: %v", err)
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		reply(w, nil, err)
		return false
	}
	return true
}

func reply(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		log.Print(err)
		w.WriteHeader(http.StatusInternalServerError)
		v = map[string]string{"error": err.Error()}
	}
	json.NewEncoder(w).Encode(v)
}

func openBrowser(url string) {
	time.Sleep(300 * time.Millisecond)
	cmd := "xdg-open"
	if runtime.GOOS == "darwin" {
		cmd = "open"
	}
	if err := exec.Command(cmd, url).Start(); err != nil {
		fmt.Println("open", url, "in your browser")
	}
}
