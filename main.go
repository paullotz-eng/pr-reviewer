package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var indexHTML []byte

func main() {
	addr := flag.String("addr", "127.0.0.1:7777", "listen address")
	repo := flag.String("repo", "", "owner/name to list PRs from (default: PRs where your review is requested)")
	limit := flag.Int("limit", 30, "number of PRs to list")
	model := flag.String("model", "", "claude model to review with (default: claude CLI default)")
	team := flag.String("team", "eversport/team-ball-racket-sports", "org/team-slug whose PRs get a team tag (\"\" to disable)")
	skillFile := flag.String("skill-file", filepath.Join(os.Getenv("HOME"), "Code/eversports", conventionsSkill),
		"local react-conventions skill, used if the PR's repo has none")
	noOpen := flag.Bool("no-open", false, "don't open the browser")
	flag.Parse()

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

	var mu sync.Mutex
	reviews := map[string]*Review{} // "repo#n" -> last review

	http.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})

	http.HandleFunc("GET /api/prs", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		prs, err := listPRs(ctx, *repo, org, strings.TrimSpace(r.URL.Query().Get("q")), *limit)
		for i := range prs {
			prs[i].Mine = prs[i].Author == me
			prs[i].Team = members[prs[i].Author]
		}
		reply(w, prs, err)
	})

	http.HandleFunc("POST /api/review", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Repo   string `json:"repo"`
			Number int    `json:"number"`
			Force  bool   `json:"force"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			reply(w, nil, err)
			return
		}
		key := req.Repo + "#" + strconv.Itoa(req.Number)
		mu.Lock()
		cached := reviews[key]
		mu.Unlock()
		if cached != nil && !req.Force {
			reply(w, cached, nil)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
		defer cancel()
		log.Printf("reviewing %s ...", key)
		start := time.Now()
		rev, err := runReview(ctx, *model, *skillFile, req.Repo, req.Number)
		if err == nil {
			log.Printf("reviewed %s in %s: %d comments", key, time.Since(start).Round(time.Second), len(rev.Comments))
			mu.Lock()
			reviews[key] = rev
			mu.Unlock()
		}
		reply(w, rev, err)
	})

	http.HandleFunc("POST /api/comment", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Repo    string `json:"repo"`
			Number  int    `json:"number"`
			HeadSHA string `json:"headSha"`
			File    string `json:"file"`
			Line    int    `json:"line"`
			Body    string `json:"body"`
			Index   int    `json:"index"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			reply(w, nil, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		url, err := postComment(ctx, req.Repo, req.Number, req.HeadSHA, req.File, req.Line, req.Body)
		if err == nil {
			log.Printf("posted comment on %s#%d: %s", req.Repo, req.Number, url)
			mu.Lock()
			if rev := reviews[req.Repo+"#"+strconv.Itoa(req.Number)]; rev != nil && req.Index >= 0 && req.Index < len(rev.Comments) {
				rev.Comments[req.Index].PostedURL = url
			}
			mu.Unlock()
		}
		reply(w, map[string]string{"url": url}, err)
	})

	url := "http://" + *addr
	log.Printf("PR reviewer running at %s", url)
	if !*noOpen {
		go openBrowser(url)
	}
	log.Fatal(http.ListenAndServe(*addr, nil))
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
