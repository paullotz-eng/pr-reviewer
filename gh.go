package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type PR struct {
	Repo      string    `json:"repo"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"createdAt"`
	URL       string    `json:"url"`
	IsDraft   bool      `json:"isDraft"`
	Team      bool      `json:"team"` // author is in your team
	Mine      bool      `json:"mine"` // author is you
}

type PRDetails struct {
	Title       string `json:"title"`
	Body        string `json:"body"`
	BaseRefName string `json:"baseRefName"`
	HeadRefName string `json:"headRefName"`
	HeadRefOid  string `json:"headRefOid"`
	Files       []struct {
		Path string `json:"path"`
	} `json:"files"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
}

func gh(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh %s: %v: %s", strings.Join(args[:min(2, len(args))], " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// listPRs returns open PRs, newest first. Without a query: the repo's PRs, or PRs
// where your review is requested. With a query: a GitHub search within the repo or org.
func listPRs(ctx context.Context, repo, org, query string, limit int) ([]PR, error) {
	args := []string{"search", "prs", "--state=open", "--sort=created", "--order=desc",
		"--limit", strconv.Itoa(limit), "--json", "number,title,author,createdAt,url,isDraft,repository"}
	switch {
	case repo != "":
		args = append(args, "--repo", repo)
	case query != "" && org != "":
		args = append(args, "--owner", org)
	case query == "":
		args = append(args, "--review-requested=@me")
	}
	if query != "" {
		args = append(args, "--", query)
	}
	out, err := gh(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Number     int       `json:"number"`
		Title      string    `json:"title"`
		CreatedAt  time.Time `json:"createdAt"`
		URL        string    `json:"url"`
		IsDraft    bool      `json:"isDraft"`
		Author     struct{ Login string }
		Repository struct{ NameWithOwner string }
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	prs := make([]PR, len(raw))
	for i, r := range raw {
		prs[i] = PR{Repo: r.Repository.NameWithOwner, Number: r.Number, Title: r.Title, Author: r.Author.Login, CreatedAt: r.CreatedAt, URL: r.URL, IsDraft: r.IsDraft}
	}
	return prs, nil
}

// teamMembers returns the logins of an "org/team-slug" GitHub team.
func teamMembers(ctx context.Context, team string) (map[string]bool, error) {
	org, slug, ok := strings.Cut(team, "/")
	if !ok {
		return nil, fmt.Errorf("team must be org/slug, got %q", team)
	}
	out, err := gh(ctx, nil, "api", "--paginate", fmt.Sprintf("orgs/%s/teams/%s/members", org, slug), "-q", ".[].login")
	if err != nil {
		return nil, err
	}
	members := map[string]bool{}
	for _, l := range strings.Fields(string(out)) {
		members[l] = true
	}
	return members, nil
}

func currentUser(ctx context.Context) (string, error) {
	out, err := gh(ctx, nil, "api", "user", "-q", ".login")
	return strings.TrimSpace(string(out)), err
}

// repoFile returns a file's raw content from a repo at a ref.
func repoFile(ctx context.Context, repo, ref, path string) (string, error) {
	out, err := gh(ctx, nil, "api", "-H", "Accept: application/vnd.github.raw",
		fmt.Sprintf("repos/%s/contents/%s?ref=%s", repo, path, ref))
	return string(out), err
}

func prDetails(ctx context.Context, repo string, n int) (*PRDetails, error) {
	out, err := gh(ctx, nil, "pr", "view", strconv.Itoa(n), "-R", repo,
		"--json", "title,body,author,baseRefName,headRefName,headRefOid,files")
	if err != nil {
		return nil, err
	}
	var d PRDetails
	return &d, json.Unmarshal(out, &d)
}

func prDiff(ctx context.Context, repo string, n int) (string, error) {
	out, err := gh(ctx, nil, "pr", "diff", strconv.Itoa(n), "-R", repo)
	return string(out), err
}

// postComment posts an inline review comment when file+line are set, falling
// back to a regular PR comment if GitHub rejects the line (e.g. not in the diff).
func postComment(ctx context.Context, repo string, n int, commitID, file string, line int, body string) (string, error) {
	if file != "" && line > 0 && commitID != "" {
		payload, _ := json.Marshal(map[string]any{
			"body": body, "commit_id": commitID, "path": file, "line": line, "side": "RIGHT",
		})
		out, err := gh(ctx, payload, "api", "-X", "POST",
			fmt.Sprintf("repos/%s/pulls/%d/comments", repo, n), "--input", "-")
		if err == nil {
			var res struct {
				HTMLURL string `json:"html_url"`
			}
			json.Unmarshal(out, &res)
			return res.HTMLURL, nil
		}
		body = fmt.Sprintf("`%s:%d`\n\n%s", file, line, body)
	}
	out, err := gh(ctx, []byte(body), "pr", "comment", strconv.Itoa(n), "-R", repo, "--body-file", "-")
	return strings.TrimSpace(string(out)), err
}
